// Автозапуск при включении машины. Всё, что раньше делалось руками по
// инструкции из README (положить файл службы, включить её, разрешить работу
// без входа в систему), здесь делает сама программа по одной галочке.
//
// Служба ставится пользовательская (systemctl --user), а не системная:
// программе не нужны права администратора, а настройки и ключи лежат в
// домашней папке. Root требуется ровно в одном месте — разрешить службе
// работать без входа пользователя в систему (loginctl enable-linger). Сначала
// пробуем без пароля: на многих системах это разрешено политикой. Не вышло —
// страница показывает поле для пароля, он используется один раз и нигде не
// сохраняется.
package webui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const unitName = "ssh_tunnel.service"

// errNeedRoot — команду не пустили без пароля. Страница по этому признаку
// показывает поле ввода, а не просто ругательство в углу.
var errNeedRoot = errors.New("нужен пароль sudo")

type bootState struct {
	// Supported — есть ли вообще о чём говорить: systemd и Linux.
	Supported bool `json:"supported"`
	// Enabled — служба включена в автозапуск.
	Enabled bool `json:"enabled"`
	// Linger — служба стартует при загрузке машины, не дожидаясь входа
	// пользователя. Без него автозапуск сработает только после логина, что на
	// сервере обычно не то, чего от него ждут.
	Linger   bool   `json:"linger"`
	UnitPath string `json:"unitPath"`
}

func currentBootState() bootState {
	st := bootState{Supported: runtime.GOOS == "linux" && haveSystemd()}
	if !st.Supported {
		return st
	}
	if p, err := unitPath(); err == nil {
		st.UnitPath = p
	}
	st.Enabled = strings.TrimSpace(output("systemctl", "--user", "is-enabled", unitName)) == "enabled"
	st.Linger = lingerOn()
	return st
}

// applyBoot включает или выключает автозапуск. password используется только
// здесь и только для sudo — он не сохраняется, не пишется в журнал и не
// возвращается обратно.
func applyBoot(enable bool, password string, flags []string) error {
	if runtime.GOOS != "linux" || !haveSystemd() {
		return errors.New("автозапуск через systemd есть только на Linux")
	}

	if !enable {
		if err := run("systemctl", "--user", "disable", unitName); err != nil {
			return fmt.Errorf("не удалось выключить автозапуск: %w", err)
		}
		return nil
	}

	if err := writeUnitIfMissing(flags); err != nil {
		return err
	}
	// Перечитать список служб полезно, но если это не вышло — ругаться будем
	// на следующем шаге, где ошибка уже по-настоящему важна.
	_ = run("systemctl", "--user", "daemon-reload")
	if err := run("systemctl", "--user", "enable", unitName); err != nil {
		return fmt.Errorf("не удалось включить автозапуск: %w", err)
	}

	// Дальше идёт то, для чего нужен root. Служба уже включена, поэтому даже
	// если пароля не дадут, автозапуск после входа в систему работает — а мы
	// честно скажем, чего именно не хватает.
	if !lingerOn() {
		if err := runRoot(password, "loginctl", "enable-linger", username()); err != nil {
			return err
		}
	}
	return nil
}

// writeUnitIfMissing кладёт файл службы, если его ещё нет. Уже существующий не
// трогаем: его мог поправить сам человек, и перезаписать это молча — худшее,
// что тут можно сделать.
func writeUnitIfMissing(flags []string) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не могу определить путь к самой программе: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("не могу создать папку для службы: %w", err)
	}
	return os.WriteFile(path, []byte(unitText(exe, flags)), 0o644)
}

func unitText(exe string, flags []string) string {
	cmd := quoteIfNeeded(exe)
	for _, f := range flags {
		cmd += " " + quoteIfNeeded(f)
	}
	return `# Служба ssh_tunnel. Файл создан самой программой по галочке
# «Запускать при старте системы» — его можно править руками, второй раз
# программа его не перезапишет.
[Unit]
Description=ssh_tunnel — SSH-туннель до собственного сервера
Documentation=https://github.com/VITAZGIO/ssh_tunel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + cmd + `
Restart=always
RestartSec=5
KillSignal=SIGTERM
TimeoutStopSec=15

[Install]
WantedBy=default.target
`
}

func quoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func unitPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user", unitName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName), nil
}

func username() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

func lingerOn() bool {
	out := output("loginctl", "show-user", username(), "--property=Linger")
	return strings.TrimSpace(out) == "Linger=yes"
}

func haveSystemd() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// runRoot выполняет команду от root. Сначала без пароля — вдруг разрешено
// политикой системы или sudo с NOPASSWD; и только если система пароль
// действительно требует, идёт в ход введённый в панели.
func runRoot(password, name string, args ...string) error {
	if err := run(name, args...); err == nil {
		return nil
	}
	if err := run("sudo", append([]string{"-n", name}, args...)...); err == nil {
		return nil
	}
	if password == "" {
		return errNeedRoot
	}
	cmd := exec.Command("sudo", append([]string{"-S", "-p", "", name}, args...)...)
	cmd.Stdin = strings.NewReader(password + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		// «Пароль не подошёл» — единственное, что стоит сказать человеку
		// напрямую: остальной вывод sudo — подсказки вида «Sorry, try again»
		// и приглашение ввести пароль ещё раз, для дела не нужны. Любую
		// другую ошибку показываем как есть — вместо голого "exit status 1"
		// в ней настоящая причина (например, не вышло достучаться до systemd).
		if strings.Contains(string(out), "incorrect password") ||
			strings.Contains(string(out), "Sorry, try again") {
			return errors.New("пароль не подошёл")
		}
		if msg := firstLine(string(out)); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// run выполняет команду и, если та ругнулась, отдаёт наружу её собственное
// сообщение. Голое «exit status 1» не говорит человеку ничего, а «Failed to
// connect to bus» — говорит всё.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := firstLine(string(out)); msg != "" {
		return errors.New(msg)
	}
	return err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func output(name string, args ...string) string {
	out, _ := exec.Command(name, args...).Output()
	return string(out)
}
