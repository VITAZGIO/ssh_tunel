package panel

import (
	"encoding/base64"
	"errors"
	"fmt"

	qrcode "github.com/skip2/go-qrcode"

	"sshtunnel/internal/share"
)

// Значения по умолчанию для конфига клиента — те же порты и режим фильтра,
// что и у нового профиля в локальной программе (internal/config.defaultProfile):
// нет смысла придумывать другие числа только потому, что профиль собирает
// панель, а не сама программа при первом запуске.
const (
	defaultClientSocksPort = 1080
	defaultClientHTTPPort  = 1081
	defaultClientPoolSize  = 4
)

// BuildClientDoc собирает файл обмена настройками (internal/share, ТЗ-01)
// для одного клиента панели: приватный ключ клиента, адрес и порт SSH,
// которые панель считает адресом самого сервера, плюс поля Panel/ClientID/
// DeviceName версии 2 формата — по ним локальная панель и приложение на
// Android узнают, что сервер выдан именно этой панелью (ТЗ-12).
//
// sshHost — обязателен: без него конфиг не говорит клиенту, куда вообще
// подключаться. Задаётся флагами -ssh-host/-domain при запуске панели
// (cmd/ssh_tunnel_panel); если ни один не задан, вызывающий код должен
// сообщить об этом человеку заранее, а не звать эту функцию с пустой
// строкой.
func BuildClientDoc(c Client, sshHost string, sshPort int, panelURL string) (share.Doc, error) {
	if sshHost == "" {
		return share.Doc{}, errors.New("не задан адрес сервера для подключения " +
			"(флаг -ssh-host или -domain при запуске панели)")
	}
	if c.PrivateKey == "" {
		return share.Doc{}, errors.New("у клиента нет сохранённого приватного ключа")
	}
	if sshPort <= 0 {
		sshPort = 22
	}
	return share.Doc{
		Name:        c.Name,
		Host:        sshHost,
		SSHPort:     sshPort,
		User:        c.Username,
		SocksPort:   defaultClientSocksPort,
		HTTPPort:    defaultClientHTTPPort,
		PoolSize:    defaultClientPoolSize,
		FilterMode:  "all",
		KeyIncluded: true,
		KeyContents: c.PrivateKey,
		Panel:       panelURL,
		ClientID:    c.ID,
		DeviceName:  c.Name,
	}, nil
}

// ClientConfigPayload — то, что нужно панели, чтобы показать блок «Показать
// настройки»: сам JSON для копирования/скачивания, PNG QR-кода в base64 (для
// сканирования телефоном) и те же значения текстом — на случай, если их
// проще вписать руками, чем сканировать или импортировать файлом.
type ClientConfigPayload struct {
	JSON        string `json:"json"`
	QRPngBase64 string `json:"qrPngBase64"`
	Host        string `json:"host"`
	SSHPort     int    `json:"sshPort"`
	User        string `json:"user"`
	PrivateKey  string `json:"privateKey"`
}

// BuildClientConfigPayload собирает всё, что нужно /api/clients/config
// (server.go) для ответа странице: JSON-файл обмена, QR-код того же
// содержимого и отдельные текстовые поля для ручного ввода.
func BuildClientConfigPayload(c Client, sshHost string, sshPort int, panelURL string) (ClientConfigPayload, error) {
	doc, err := BuildClientDoc(c, sshHost, sshPort, panelURL)
	if err != nil {
		return ClientConfigPayload{}, err
	}
	payload, err := share.Build(doc)
	if err != nil {
		return ClientConfigPayload{}, err
	}
	png, err := qrcode.Encode(string(payload), qrcode.Low, 320)
	if err != nil {
		return ClientConfigPayload{}, fmt.Errorf("не удалось собрать QR-код: %w", err)
	}
	return ClientConfigPayload{
		JSON:        string(payload),
		QRPngBase64: base64.StdEncoding.EncodeToString(png),
		Host:        doc.Host,
		SSHPort:     doc.SSHPort,
		User:        doc.User,
		PrivateKey:  c.PrivateKey,
	}, nil
}
