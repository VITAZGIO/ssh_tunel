package panel

// Ёмкость сервера: сколько устройств он выдерживает одновременно.
//
// Откуда вообще брался предел. Программа держит с сервером не одно
// SSH-соединение, а пул из четырёх (tunnel.Config.PoolSize) — так один
// медленный поток не тормозит остальные. А у sshd есть защита от наплыва
// MaxStartups, по умолчанию "10:30:100": когда больше десяти соединений
// одновременно ещё не вошли (идёт рукопожатие и проверка ключа), каждое
// новое отбрасывается с вероятностью 30%, и чем больше очередь, тем чаще.
// Три устройства, которые подключаются разом (сервер перезагрузился, у всех
// моргнул интернет), — это 12 рукопожатий: часть соединений sshd молча
// сбрасывает, и четвёртое устройство уже не может войти. Отсюда «лимит в три
// устройства».
//
// Панель выставляет MaxStartups под выбранное число устройств: начало
// отбрасывания — 4 соединения на устройство, полный отказ — вдвое больше.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultMaxDevices — сколько устройств, если в настройках не задано.
	DefaultMaxDevices = 30
	// MinMaxDevices/MaxMaxDevices — границы настройки. Верхняя — не
	// ограничение программы: каждое ждущее входа соединение — отдельный
	// процесс sshd, и тысячи их разом положат маленький VPS по памяти.
	MinMaxDevices = 1
	MaxMaxDevices = 250

	// connsPerDevice — SSH-соединений у одного устройства (пул туннеля).
	connsPerDevice = 4
)

// EffectiveMaxDevices — число устройств с учётом значения по умолчанию.
func EffectiveMaxDevices(n int) int {
	if n <= 0 {
		return DefaultMaxDevices
	}
	return n
}

// ValidMaxDevices проверяет значение из интерфейса.
func ValidMaxDevices(n int) error {
	if n < MinMaxDevices || n > MaxMaxDevices {
		return fmt.Errorf("число устройств должно быть от %d до %d", MinMaxDevices, MaxMaxDevices)
	}
	return nil
}

// MaxStartupsFor — значение MaxStartups для n устройств. Меньше умолчаний
// sshd (10:30:100) не опускаемся: для одного-двух устройств запас не мешает.
func MaxStartupsFor(n int) string {
	begin := n * connsPerDevice
	if begin < 10 {
		begin = 10
	}
	full := begin * 2
	if full < 100 {
		full = 100
	}
	return fmt.Sprintf("%d:30:%d", begin, full)
}

const (
	capacityBegin = "# BEGIN ssh_tunnel_panel capacity (правь через панель: Настройки → Ёмкость сервера)"
	capacityEnd   = "# END ssh_tunnel_panel capacity"
	// disabledPrefix — так же гасит чужие строки скрипт настройки сервера
	// (harden.sh): одинаковая пометка — понятно, кто и зачем.
	disabledPrefix = "# выключено ssh_tunnel: "
)

var maxStartupsLine = regexp.MustCompile(`(?i)^\s*MaxStartups\s`)

// sshdFile — файл настроек sshd в памяти.
type sshdFile struct {
	Path string
	Text string
}

// planCapacity — чистая функция: что поменять в файлах sshd, чтобы
// действовало наше MaxStartups. Возвращает только изменившиеся файлы.
//
// sshd берёт ПЕРВОЕ встреченное значение настройки, а MaxStartups —
// глобальная и внутри Match не допускается. Поэтому наш блок стоит в самом
// начале основного файла (раньше любого Include и Match), а все прочие
// объявления, где бы они ни были, гасятся — иначе строка из образа хостинга
// или от старого скрипта тихо перебила бы выбранное в панели.
func planCapacity(main sshdFile, dropIns []sshdFile, devices int) []sshdFile {
	block := capacityBegin + "\n" +
		"# " + strconv.Itoa(devices) + " устройств × " + strconv.Itoa(connsPerDevice) + " SSH-соединения\n" +
		"MaxStartups " + MaxStartupsFor(devices) + "\n" +
		capacityEnd + "\n"

	var changed []sshdFile
	newMain := block + disableMaxStartups(stripCapacityBlock(main.Text))
	if newMain != main.Text {
		changed = append(changed, sshdFile{Path: main.Path, Text: newMain})
	}
	for _, f := range dropIns {
		if t := disableMaxStartups(f.Text); t != f.Text {
			changed = append(changed, sshdFile{Path: f.Path, Text: t})
		}
	}
	return changed
}

// stripCapacityBlock убирает наш прошлый блок (и пустую строку после него).
func stripCapacityBlock(text string) string {
	i := strings.Index(text, capacityBegin)
	if i < 0 {
		return text
	}
	j := strings.Index(text[i:], capacityEnd)
	if j < 0 {
		return text
	}
	end := i + j + len(capacityEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return text[:i] + text[end:]
}

// disableMaxStartups гасит все действующие строки MaxStartups.
func disableMaxStartups(text string) string {
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		if maxStartupsLine.MatchString(l) {
			lines[i] = disabledPrefix + strings.TrimLeft(l, " \t")
		}
	}
	return strings.Join(lines, "")
}

// SSHDDropInDir — папка дополнительных файлов sshd. Переменная — для тестов.
var SSHDDropInDir = "/etc/ssh/sshd_config.d"

// sshdCheck и sshdReload — вызовы настоящего sshd; тесты подменяют их.
var (
	sshdCheck = func(configPath string) error {
		if _, err := exec.LookPath("sshd"); err != nil {
			return errors.New("не нашёл sshd в PATH — без проверки sshd -t менять конфиг вслепую нельзя")
		}
		if out, err := exec.Command("sshd", "-t", "-f", configPath).CombinedOutput(); err != nil {
			return fmt.Errorf("sshd -t: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	sshdReload = reloadSSHD
)

// ApplyCapacity выставляет sshd под devices устройств. Если всё уже так —
// ничего не трогает и sshd не перечитывает.
//
// Меняться может сразу несколько файлов, поэтому порядок такой: прежнее
// содержимое в памяти → запись → sshd -t на итоговой конфигурации целиком →
// при ошибке всё возвращается как было, и только при успехе sshd
// перечитывает настройки. Уже открытые соединения это не рвёт.
func ApplyCapacity(devices int) error {
	if err := ValidMaxDevices(devices); err != nil {
		return err
	}
	mainText, err := os.ReadFile(SSHDPath)
	if err != nil {
		return fmt.Errorf("не могу прочитать %s: %w", SSHDPath, err)
	}
	paths, _ := filepath.Glob(filepath.Join(SSHDDropInDir, "*.conf"))
	sort.Strings(paths)
	var dropIns []sshdFile
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("не могу прочитать %s: %w", p, err)
		}
		dropIns = append(dropIns, sshdFile{Path: p, Text: string(data)})
	}

	changed := planCapacity(sshdFile{Path: SSHDPath, Text: string(mainText)}, dropIns, devices)
	if len(changed) == 0 {
		return nil
	}
	original := map[string]string{SSHDPath: string(mainText)}
	for _, f := range dropIns {
		original[f.Path] = f.Text
	}

	var written []string
	restore := func() {
		for _, p := range written {
			writeKeepMode(p, original[p])
		}
	}
	for _, f := range changed {
		if err := writeKeepMode(f.Path, f.Text); err != nil {
			restore()
			return fmt.Errorf("не могу записать %s: %w", f.Path, err)
		}
		written = append(written, f.Path)
	}
	if err := sshdCheck(SSHDPath); err != nil {
		restore()
		return fmt.Errorf("новые настройки sshd не прошли проверку, всё возвращено как было: %w", err)
	}
	return sshdReload()
}

// writeKeepMode пишет файл через временный рядом и сохраняет права.
func writeKeepMode(path, text string) error {
	var mode os.FileMode = 0o644
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := path + ".ssh_tunnel_panel.tmp"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// sshdEffective — что sshd на самом деле применяет (sshd -T), чтобы панель
// показывала правду, а не только то, что сама записала.
var sshdEffective = func(key string) string {
	if _, err := exec.LookPath("sshd"); err != nil {
		return ""
	}
	out, err := exec.Command("sshd", "-T").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && strings.EqualFold(f[0], key) {
			return f[1]
		}
	}
	return ""
}

// CapacityStatus — то, что показывает интерфейс.
type CapacityStatus struct {
	MaxDevices int    `json:"maxDevices"`
	Min        int    `json:"min"`
	Max        int    `json:"max"`
	Default    int    `json:"default"`
	PerDevice  int    `json:"perDevice"`
	Wanted     string `json:"wanted"`
	Effective  string `json:"effective,omitempty"`
}

// Capacity — текущее состояние ёмкости.
func Capacity(devices int) CapacityStatus {
	devices = EffectiveMaxDevices(devices)
	return CapacityStatus{
		MaxDevices: devices,
		Min:        MinMaxDevices,
		Max:        MaxMaxDevices,
		Default:    DefaultMaxDevices,
		PerDevice:  connsPerDevice,
		Wanted:     MaxStartupsFor(devices),
		Effective:  sshdEffective("maxstartups"),
	}
}
