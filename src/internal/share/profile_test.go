package share

import "testing"

// Build -> Parse должен вернуть то же самое, что положили, и всегда
// проставлять актуальную версию формата.
func TestBuildParseRoundTrip(t *testing.T) {
	doc := Doc{
		Name: "Амстердам", Flag: "NL", Host: "203.0.113.10", SSHPort: 22, User: "tunnel",
		SocksPort: 1080, HTTPPort: 1081, PoolSize: 4, FilterMode: "all",
		FilterApps: []string{"com.example.app"}, DirectHosts: []string{"example.com"},
		LocalViaTunnel: true, KeyIncluded: true, KeyContents: "fakekeyfakekey",
		Panel: "https://panel.example.com", ClientID: "client-1", DeviceName: "Pixel 8",
	}

	data, err := Build(doc)
	if err != nil {
		t.Fatalf("Build вернул ошибку: %v", err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse вернул ошибку: %v", err)
	}

	if got.Format != CurrentFormat {
		t.Errorf("Format = %d, хотели %d", got.Format, CurrentFormat)
	}
	switch {
	case got.Name != doc.Name, got.Flag != doc.Flag, got.Host != doc.Host,
		got.SSHPort != doc.SSHPort, got.User != doc.User, got.SocksPort != doc.SocksPort,
		got.HTTPPort != doc.HTTPPort, got.PoolSize != doc.PoolSize, got.FilterMode != doc.FilterMode,
		got.LocalViaTunnel != doc.LocalViaTunnel, got.KeyIncluded != doc.KeyIncluded,
		got.KeyContents != doc.KeyContents, got.Panel != doc.Panel, got.ClientID != doc.ClientID,
		got.DeviceName != doc.DeviceName:
		t.Errorf("после круга экспорт-импорт данные разошлись:\nбыло: %+v\nстало: %+v", doc, got)
	}
}

// Файл версии 1 (без новых полей) должен читаться без ошибок, а новые поля
// в результате остаются пустыми — старые файлы, разосланные людям, не
// должны ломаться.
func TestParseVersion1File(t *testing.T) {
	v1 := []byte(`{"sshTunnelExport":1,"name":"Старый","host":"198.51.100.9","sshPort":22,
		"user":"tunnel","socksPort":1080,"httpPort":1081,"poolSize":4,"filterMode":"all",
		"keyIncluded":false}`)

	doc, err := Parse(v1)
	if err != nil {
		t.Fatalf("файл версии 1 не должен вызывать ошибку: %v", err)
	}
	if doc.Host != "198.51.100.9" || doc.Name != "Старый" {
		t.Errorf("основные поля версии 1 не разобрались: %+v", doc)
	}
	if doc.Panel != "" || doc.ClientID != "" || doc.DeviceName != "" {
		t.Errorf("в файле версии 1 не было новых полей, а они не пустые: %+v", doc)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	cases := []string{
		`не json вообще`,
		`{"foo":"bar"}`,       // валидный JSON, но не экспорт ssh_tunnel
		`{"host":""}`,         // адрес пустой
		`{"sshTunnelExport"}`, // сломанный JSON
	}
	for _, c := range cases {
		if _, err := Parse([]byte(c)); err == nil {
			t.Errorf("Parse(%q) должен был вернуть ошибку", c)
		}
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	for _, c := range []string{"", "   ", "\n"} {
		if _, err := Parse([]byte(c)); err == nil {
			t.Errorf("Parse(%q) должен был вернуть ошибку про пустой файл", c)
		}
	}
}
