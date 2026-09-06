package panel

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/share"
)

func testClientWithKey() Client {
	return Client{
		ID:         "tun_0123456789abcdef",
		Name:       "Ноутбук",
		DeviceType: DeviceLinux,
		CreatedAt:  time.Now(),
		State:      StateActive,
		Username:   "tun_0123456789abcdef",
		PublicKey:  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBhq",
		PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nZm9v\n-----END OPENSSH PRIVATE KEY-----\n",
	}
}

func TestBuildClientDocFillsShareDoc(t *testing.T) {
	c := testClientWithKey()
	doc, err := BuildClientDoc(c, "vps.example.com", 2222, "https://panel.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Host != "vps.example.com" || doc.SSHPort != 2222 {
		t.Fatalf("неверный адрес/порт сервера: %+v", doc)
	}
	if doc.User != c.Username {
		t.Fatalf("пользователь должен быть unix-именем клиента: %q != %q", doc.User, c.Username)
	}
	if !doc.KeyIncluded || doc.KeyContents != c.PrivateKey {
		t.Fatalf("приватный ключ должен попасть в конфиг: %+v", doc)
	}
	if doc.Panel != "https://panel.example.com/" {
		t.Fatalf("поле Panel должно быть адресом панели, получил %q", doc.Panel)
	}
	if doc.ClientID != c.ID {
		t.Fatalf("ClientID должен совпадать с id клиента, получил %q", doc.ClientID)
	}
	if doc.DeviceName != c.Name {
		t.Fatalf("DeviceName должен совпадать с именем клиента, получил %q", doc.DeviceName)
	}
}

func TestBuildClientDocDefaultsPort(t *testing.T) {
	c := testClientWithKey()
	doc, err := BuildClientDoc(c, "vps.example.com", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if doc.SSHPort != 22 {
		t.Fatalf("нулевой порт должен подставиться как 22, получил %d", doc.SSHPort)
	}
}

func TestBuildClientDocRequiresHost(t *testing.T) {
	c := testClientWithKey()
	if _, err := BuildClientDoc(c, "", 22, ""); err == nil {
		t.Fatal("пустой адрес сервера должен быть ошибкой")
	}
}

func TestBuildClientDocRequiresPrivateKey(t *testing.T) {
	c := testClientWithKey()
	c.PrivateKey = ""
	if _, err := BuildClientDoc(c, "vps.example.com", 22, ""); err == nil {
		t.Fatal("отсутствие приватного ключа должно быть ошибкой")
	}
}

func TestBuildClientConfigPayloadRoundTripsThroughShareParse(t *testing.T) {
	c := testClientWithKey()
	payload, err := BuildClientConfigPayload(c, "vps.example.com", 22, "https://panel.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if payload.Host != "vps.example.com" || payload.SSHPort != 22 || payload.User != c.Username {
		t.Fatalf("неверные текстовые поля: %+v", payload)
	}
	if payload.PrivateKey != c.PrivateKey {
		t.Fatal("приватный ключ в текстовом поле должен совпадать с ключом клиента")
	}
	if payload.JSON == "" {
		t.Fatal("payload.JSON не должен быть пустым")
	}

	// То, что попадает в QR и в кнопку «Скопировать», должно разбираться
	// обратно тем же кодом, что разбирает файл импорта в локальной панели —
	// это один и тот же формат (internal/share, ТЗ-01).
	doc, err := share.Parse([]byte(payload.JSON))
	if err != nil {
		t.Fatalf("payload.JSON не прошёл share.Parse: %v", err)
	}
	if doc.ClientID != c.ID || doc.DeviceName != c.Name || doc.Panel != "https://panel.example.com/" {
		t.Fatalf("после share.Parse потерялись поля версии 2: %+v", doc)
	}
	if !doc.KeyIncluded || doc.KeyContents != c.PrivateKey {
		t.Fatalf("после share.Parse потерялся приватный ключ: %+v", doc)
	}

	if payload.QRPngBase64 == "" {
		t.Fatal("QR-код не должен быть пустым")
	}
	png, err := base64.StdEncoding.DecodeString(payload.QRPngBase64)
	if err != nil {
		t.Fatalf("QR-код должен быть валидным base64: %v", err)
	}
	if !strings.HasPrefix(string(png), "\x89PNG") {
		t.Fatal("QR-код должен быть настоящим PNG")
	}
}

func TestBuildClientConfigPayloadFailsWithoutHost(t *testing.T) {
	c := testClientWithKey()
	if _, err := BuildClientConfigPayload(c, "", 22, ""); err == nil {
		t.Fatal("без адреса сервера сборка конфига должна отдавать ошибку")
	}
}
