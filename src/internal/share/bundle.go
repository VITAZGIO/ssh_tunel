package share

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// BundleFormat — версия формата полной выгрузки настроек (все серверы плюс
// общие галочки). Отдельно от Doc: тот описывает ОДИН сервер и уже разослан
// людям файлами, ломать его нельзя.
const BundleFormat = 1

// Bundle — «все настройки целиком», чтобы перенести программу на другой
// компьютер или телефон одной вставкой из буфера. Списки блокировки рекламы
// сюда намеренно не входят: их десятки тысяч строк, и текст перестал бы
// влезать в сообщение мессенджера. Белый список (исключения) маленький и
// переносится — без него блокировка на новом устройстве ломала бы те же
// сайты, что и раньше.
type Bundle struct {
	Format int `json:"sshTunnelBackup"`

	// Servers — те же документы обмена, что и у экспорта одного сервера, со
	// всеми ключами: иначе на новом устройстве пришлось бы заново раздавать
	// доступ к каждому серверу.
	Servers []Doc `json:"servers"`
	// Active — номер сервера в Servers, который был выбран. -1, если неясно.
	Active int `json:"active"`

	Language         string `json:"language,omitempty"`
	Verbose          bool   `json:"verbose"`
	AutoStart        bool   `json:"autoStart"`
	AutoConnect      *bool  `json:"autoConnect,omitempty"`
	ShowServerPicker bool   `json:"showServerPicker"`
	AutoPickFastest  bool   `json:"autoPickFastest"`
	SysProxy         bool   `json:"sysProxy"`
	SetEnvVars       bool   `json:"setEnvVars"`

	// Mobile — раздел телефона (DNS, режим блокировки рекламы, белый список,
	// выбранные приложения). Компьютер его не понимает, но и не теряет:
	// сырой JSON, который просто перекладывается дальше.
	Mobile json.RawMessage `json:"mobile,omitempty"`
}

// BuildBundle собирает выгрузку. Format проставляется сам — как и в Build.
func BuildBundle(b Bundle) ([]byte, error) {
	b.Format = BundleFormat
	if b.Servers == nil {
		b.Servers = []Doc{}
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("не удалось собрать выгрузку настроек: %w", err)
	}
	return data, nil
}

// ParseBundle разбирает выгрузку. Отдельно от Parse: человек легко перепутает
// экспорт одного сервера с полной выгрузкой, и сообщение должно объяснять,
// что именно он вставил, а не «не разобрал файл».
func ParseBundle(data []byte) (Bundle, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return Bundle{}, errors.New("пусто: скопируй настройки на старом устройстве и вставь сюда")
	}
	var b Bundle
	if err := json.Unmarshal([]byte(text), &b); err != nil {
		return Bundle{}, fmt.Errorf("не удалось разобрать настройки: %w", err)
	}
	if b.Format == 0 {
		// Похоже на экспорт одного сервера — подсказываем, куда его вставлять.
		var one Doc
		if json.Unmarshal([]byte(text), &one) == nil && one.Host != "" {
			return Bundle{}, errors.New("это настройки одного сервера, а не всех: " +
				"вставь их кнопкой «Вставить сервер из буфера»")
		}
		return Bundle{}, errors.New("это не выгрузка настроек ssh_tunnel")
	}
	if len(b.Servers) == 0 {
		return Bundle{}, errors.New("в выгрузке нет ни одного сервера")
	}
	return b, nil
}
