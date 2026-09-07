package panel

import (
	"fmt"
	"os/exec"
	"strings"
)

// Автозапуск панели после перезагрузки сервера.
//
// Технически это ровно то же самое, что «systemctl enable/disable
// ssh_tunnel_panel» руками в консоли — панель просто вызывает systemctl за
// человека, чтобы галочку можно было снять из интерфейса, а не идти на
// сервер по SSH. Останавливать саму себя панель не умеет и не должна:
// disable влияет только на следующую загрузку машины, текущий запуск
// продолжает работать.

// systemdUnitName — имя юнита, которое ставит packaging/panel/install.sh.
const systemdUnitName = "ssh_tunnel_panel"

// runSystemctl вынесен в переменную, чтобы тесты могли подменить вызов
// системы: настоящий systemctl в песочнице тестов недоступен, а поведение
// разбора его ответов проверить надо.
var runSystemctl = func(args ...string) (string, error) {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return "", errNoSystemctl
	}
	out, err := exec.Command(path, args...).CombinedOutput()
	return string(out), err
}

var errNoSystemctl = fmt.Errorf("systemctl не найден")

// AutostartStatus — что показать в настройках панели.
type AutostartStatus struct {
	// Supported — есть ли вообще systemd с нашим юнитом. Панель может быть
	// запущена руками из консоли или в контейнере — тогда галочке нечем
	// управлять, и интерфейс её просто не показывает.
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
}

// Autostart возвращает текущее состояние автозапуска.
//
// systemctl is-enabled отвечает не только словом, но и кодом возврата
// (disabled — это ненулевой код), поэтому ошибка выполнения сама по себе ещё
// не значит, что что-то сломалось: сначала смотрим на вывод.
func Autostart() AutostartStatus {
	out, err := runSystemctl("is-enabled", systemdUnitName)
	word := strings.TrimSpace(out)
	switch word {
	case "enabled", "enabled-runtime", "alias", "static", "indirect":
		return AutostartStatus{Supported: true, Enabled: true}
	case "disabled", "masked", "masked-runtime":
		return AutostartStatus{Supported: true, Enabled: false}
	}
	// Ни одного знакомого слова: нет systemctl, нет такого юнита ("Failed to
	// get unit file state ... No such file or directory") — управлять нечем.
	_ = err
	return AutostartStatus{Supported: false}
}

// SetAutostart включает или выключает автозапуск на следующих загрузках.
func SetAutostart(enabled bool) error {
	verb := "disable"
	if enabled {
		verb = "enable"
	}
	out, err := runSystemctl(verb, systemdUnitName)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("systemctl %s %s: %s", verb, systemdUnitName, msg)
	}
	return nil
}
