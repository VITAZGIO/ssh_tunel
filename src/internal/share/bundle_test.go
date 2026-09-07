package share

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBundleПереживаетКруг(t *testing.T) {
	yes := true
	src := Bundle{
		Servers: []Doc{
			{Name: "Амстердам", Host: "1.2.3.4", SSHPort: 22, User: "tun_a",
				KeyIncluded: true, KeyContents: "KEY-A"},
			{Name: "Франкфурт", Host: "5.6.7.8", SSHPort: 22, User: "tun_b"},
		},
		Active: 1, Language: "ru", AutoStart: true, AutoConnect: &yes,
		ShowServerPicker: true, AutoPickFastest: true,
		Mobile: json.RawMessage(`{"dns":"1.1.1.1"}`),
	}
	data, err := BuildBundle(src)
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	got, err := ParseBundle(data)
	if err != nil {
		t.Fatalf("ParseBundle: %v", err)
	}
	if got.Format != BundleFormat {
		t.Errorf("формат %d, ожидался %d", got.Format, BundleFormat)
	}
	if len(got.Servers) != 2 || got.Servers[0].KeyContents != "KEY-A" {
		t.Errorf("серверы приехали не целиком: %+v", got.Servers)
	}
	if got.Active != 1 || got.Language != "ru" || !got.AutoPickFastest {
		t.Errorf("общие настройки потерялись: %+v", got)
	}
	// MarshalIndent переформатирует вложенный сырой JSON, поэтому сравниваем
	// по содержимому, а не по тексту.
	var mobile map[string]string
	if err := json.Unmarshal(got.Mobile, &mobile); err != nil || mobile["dns"] != "1.1.1.1" {
		t.Errorf("раздел телефона потерялся: %s (%v)", got.Mobile, err)
	}
}

// Самая частая ошибка человека — вставить в «импорт всех настроек» экспорт
// одного сервера. Сообщение должно объяснять, куда его вставлять.
func TestParseBundleУзнаётЭкспортОдногоСервера(t *testing.T) {
	one, err := Build(Doc{Name: "Амстердам", Host: "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseBundle(one)
	if err == nil || !strings.Contains(err.Error(), "одного сервера") {
		t.Errorf("получено %v, ожидалась подсказка про один сервер", err)
	}
}

func TestParseBundleОтвергаетПустоеИЧужое(t *testing.T) {
	if _, err := ParseBundle([]byte("   ")); err == nil {
		t.Error("пустая вставка должна быть ошибкой")
	}
	if _, err := ParseBundle([]byte(`{"что-то":1}`)); err == nil {
		t.Error("чужой JSON должен быть ошибкой")
	}
	if _, err := ParseBundle([]byte(`{"sshTunnelBackup":1,"servers":[]}`)); err == nil {
		t.Error("выгрузка без серверов должна быть ошибкой")
	}
}
