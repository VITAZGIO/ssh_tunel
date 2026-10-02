// Пакет winsvc — служба Windows для версии с режимом VPN.
//
// Зачем служба. VPN-версии нужны права администратора, и обычная автозагрузка
// её не поднимает. А главное — сеть устройств должна работать, пока на
// компьютере никто не вошёл в систему: включил машину удалённо, она висит на
// экране ввода пароля, а к ней уже можно подключиться по RDP. Так это устроено
// у NetBird и Tailscale: ядро — служба Windows (запускается системой при
// включении, от имени SYSTEM), окно — отдельная программа без прав
// администратора, которая только показывает и управляет.
//
// Здесь так же, но exe один: ssh_tunnel_vpn.exe -service — это служба (ядро,
// адаптер, сеть устройств и веб-интерфейс на 127.0.0.1), обычный запуск —
// окно, которое показывает интерфейс службы. Как окно находит службу — файл
// service.json в папке настроек пользователя (см. Endpoint).
//
// Раскладка: endpoint.go — файл service.json (без вызовов Windows, проверяется
// тестом где угодно), service_windows.go — установка и управление службой,
// elevate_windows.go — запуск себя с правами администратора (окно UAC),
// runkey_windows.go — окно в автозагрузке пользователя.
package winsvc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Name — имя службы в «Службах» Windows (services.msc) и для sc.exe.
	Name = "ssh_tunnel_vpn"
	// DisplayName — как служба называется в списке.
	DisplayName = "ssh_tunnel VPN"
	// Description — пояснение в свойствах службы.
	Description = "VPN и сеть устройств ssh_tunnel. Поднимается при включении компьютера, до входа в систему."
)

// Endpoint — где служба слушает свой веб-интерфейс и с каким ключом.
// Служба пишет его при каждом запуске (порт и ключ каждый раз новые), окно
// читает. Лежит в папке настроек пользователя: туда могут заглянуть только
// он сам, администраторы и SYSTEM — посторонний на том же компьютере ключа
// не прочитает.
type Endpoint struct {
	Addr    string `json:"addr"`
	Token   string `json:"token"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

// EndpointPath — путь к service.json в папке настроек dir.
func EndpointPath(dir string) string { return filepath.Join(dir, "service.json") }

// WriteEndpoint записывает файл атомарно: окно не должно прочитать
// половину.
func WriteEndpoint(dir string, e Endpoint) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := EndpointPath(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ReadEndpoint читает service.json. Ошибка — файла нет или он испорчен.
func ReadEndpoint(dir string) (Endpoint, error) {
	var e Endpoint
	data, err := os.ReadFile(EndpointPath(dir))
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, err
	}
	if e.Addr == "" || e.Token == "" {
		return e, errors.New("service.json неполный")
	}
	return e, nil
}

// RemoveEndpoint убирает файл при остановке службы: устаревший адрес окну
// только мешал бы.
func RemoveEndpoint(dir string) { os.Remove(EndpointPath(dir)) }

// runEntryExe — путь к программе из строки запуска: в кавычках или до
// первого пробела.
func runEntryExe(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.Index(cmd[1:], `"`); end >= 0 {
			return cmd[1 : end+1]
		}
		return strings.Trim(cmd, `"`)
	}
	if i := strings.IndexByte(cmd, ' '); i > 0 {
		return cmd[:i]
	}
	return cmd
}
