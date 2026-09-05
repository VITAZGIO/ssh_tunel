package panel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// sshdGroup — группа, на которую вешаются ограничения sshd. Все клиенты
// панели состоят в ней и только в ней (см. provision.go) — сама группа
// вообще ни на что в системе не влияет, кроме того, что для неё сказано в
// Match-блоке ниже.
const sshdGroup = "sshtunnel"

// sshdMarker — начало и конец блока, который панель добавляет в
// sshd_config. Отдельные маркеры, а не просто "Match Group sshtunnel",
// нужны затем, чтобы ensureMatchBlock мог надёжно узнать «этот блок уже мой»
// и не добавлять его второй раз, даже если кто-то поправил текст между
// маркерами руками.
const (
	sshdMarkerBegin = "# BEGIN ssh_tunnel_panel (не редактируй руками — правь через панель)"
	sshdMarkerEnd   = "# END ssh_tunnel_panel"
)

// sshdMatchBlock — сам блок ограничений. Разрешён только проброс портов;
// интерактивная сессия, агент, X11 и туннели уровня L3 (PermitTunnel)
// закрыты. ForceCommand на /usr/sbin/nologin — подстраховка на случай,
// если кто-то всё же попадёт в сессию: команда завершится тем же самым
// сообщением, что видит любой замороженный обычный пользователь.
//
// Это ограничение накладывается на группу целиком одним блоком в конце
// файла (см. ensureMatchBlock) — панель правит его один раз при первом
// запуске и после не трогает при добавлении отдельных клиентов: у каждого
// клиента вместо этого просто свой ключ в authorized_keys (см.
// authorizedkeys.go).
func sshdMatchBlock() string {
	return sshdMarkerBegin + "\n" +
		"Match Group " + sshdGroup + "\n" +
		"    PermitTTY no\n" +
		"    AllowAgentForwarding no\n" +
		"    PermitTunnel no\n" +
		"    X11Forwarding no\n" +
		"    AllowTcpForwarding yes\n" +
		"    ForceCommand /usr/sbin/nologin\n" +
		sshdMarkerEnd + "\n"
}

// ensureMatchBlock — чистая функция без обращений к диску: по тексту
// текущего sshd_config решает, нужно ли дописать блок ограничений, и если
// да — возвращает готовый новый текст. changed=false означает «блок уже
// есть, файл трогать не надо» — applySSHDConfig в этом случае вообще не
// станет писать на диск и перезапускать sshd.
func ensureMatchBlock(config string) (updated string, changed bool) {
	if strings.Contains(config, sshdMarkerBegin) {
		return config, false
	}
	out := config
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out != "" {
		out += "\n"
	}
	out += sshdMatchBlock()
	return out, true
}

// SSHDPath — путь к конфигу sshd. Переменная, а не константа, чтобы тесты
// на applySSHDConfig (если понадобятся) могли подменить её без реального
// /etc.
var SSHDPath = "/etc/ssh/sshd_config"

// EnsureSSHDRestrictions один раз за время жизни процесса панели проверяет,
// что в sshd_config есть блок ограничений для группы sshtunnel, и при
// необходимости дописывает его. Вызывается при старте программы — не при
// добавлении клиента: конфиг sshd правится ровно один раз, а не на каждого
// нового клиента.
//
// Перед тем как заменить настоящий файл, новый текст проверяется командой
// "sshd -t" на отдельном временном файле: сломанный sshd_config опаснее,
// чем панель без только что добавленного ограничения, поэтому при ошибке
// проверки исходный файл не трогается вовсе.
func EnsureSSHDRestrictions() error {
	current, err := os.ReadFile(SSHDPath)
	if err != nil {
		return fmt.Errorf("не могу прочитать %s: %w", SSHDPath, err)
	}
	updated, changed := ensureMatchBlock(string(current))
	if !changed {
		return nil
	}
	return applySSHDConfig(updated)
}

// applySSHDConfig — единственное место, где панель реально пишет в
// sshd_config. Порядок специально осторожный: временный файл -> проверка
// синтаксиса -> атомарная замена -> перечитывание службы; на любой ошибке
// раньше замены исходный конфиг остаётся как был.
func applySSHDConfig(newConfig string) error {
	if _, err := exec.LookPath("sshd"); err != nil {
		return errors.New("не нашёл sshd в PATH — проверка нового конфига пропущена, " +
			"конфиг не тронут: без sshd -t менять его вслепую нельзя")
	}

	info, err := os.Stat(SSHDPath)
	var mode os.FileMode = 0o644
	if err == nil {
		mode = info.Mode()
	}

	tmp := SSHDPath + ".ssh_tunnel_panel.tmp"
	if err := os.WriteFile(tmp, []byte(newConfig), mode); err != nil {
		return fmt.Errorf("не могу записать временный конфиг: %w", err)
	}
	defer os.Remove(tmp)

	if out, err := exec.Command("sshd", "-t", "-f", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("новый sshd_config не проходит проверку sshd -t, конфиг не тронут: %s",
			strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, SSHDPath); err != nil {
		return fmt.Errorf("не могу заменить %s: %w", SSHDPath, err)
	}
	return reloadSSHD()
}

// reloadSSHD перечитывает конфиг живой службы. Пробует оба распространённых
// имени юнита — на Debian/Ubuntu служба называется "ssh", на большинстве
// остальных дистрибутивов — "sshd".
func reloadSSHD() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("конфиг sshd обновлён, но не нашёл systemctl — перечитай службу sshd руками")
	}
	var lastErr error
	for _, unit := range []string{"ssh", "sshd"} {
		out, err := exec.Command("systemctl", "reload", unit).CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("systemctl reload %s: %s", unit, strings.TrimSpace(string(out)))
	}
	return fmt.Errorf("конфиг sshd обновлён, но не удалось перечитать службу — примени руками: %w", lastErr)
}
