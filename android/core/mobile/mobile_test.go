package mobile

import "testing"

// ParseConfig — то, что вызывает Android при вставке из буфера или сканировании
// QR: тот же разбор, что у экспорта/импорта на компьютере (internal/share),
// но с плоскими полями, которые понимает gomobile.
func TestParseConfigRoundTrip(t *testing.T) {
	text := `{"sshTunnelExport":2,"name":"Амстердам","flag":"NL","host":"203.0.113.10",
		"sshPort":22,"user":"tunnel","socksPort":1080,"httpPort":1081,"poolSize":4,
		"filterMode":"only","filterApps":["com.android.chrome","com.example.app"],
		"directHosts":["10.8.0.0/16"],"localViaTunnel":true,
		"keyIncluded":true,"keyContents":"fakekeyfakekey"}`

	got, err := ParseConfig(text)
	if err != nil {
		t.Fatalf("ParseConfig вернул ошибку: %v", err)
	}
	if got.Host != "203.0.113.10" || got.User != "tunnel" || got.SshPort != 22 {
		t.Errorf("основные поля не разобрались: %+v", got)
	}
	if got.FilterApps != "com.android.chrome\ncom.example.app" {
		t.Errorf("FilterApps должен быть строкой по одному значению на строку, получили: %q", got.FilterApps)
	}
	if got.DirectHosts != "10.8.0.0/16" {
		t.Errorf("DirectHosts не разобрался: %q", got.DirectHosts)
	}
	if !got.KeyIncluded || got.KeyContents != "fakekeyfakekey" {
		t.Errorf("ключ не разобрался: %+v", got)
	}
}

func TestParseConfigRejectsGarbage(t *testing.T) {
	if _, err := ParseConfig("это не конфиг"); err == nil {
		t.Error("ParseConfig должен вернуть ошибку на мусор во входе")
	}
	if _, err := ParseConfig(""); err == nil {
		t.Error("ParseConfig должен вернуть ошибку на пустой ввод")
	}
}
