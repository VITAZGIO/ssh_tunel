package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sshtunnel/internal/app"
	"sshtunnel/internal/share"
)

// Оба варианта — «ключ этого компьютера» и «отдельный ключ» — должны отдавать
// валидный JSON с содержимым ключа, но только «отдельный ключ» даёт публичный
// ключ для команды authorized_keys на сервере.
func TestAndroidKeyModes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)

	keyPath := filepath.Join(tmp, "id_test")
	keyBody := "-----BEGIN OPENSSH PRIVATE KEY-----\nfakekeyfakekey\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(keyPath, []byte(keyBody), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := withHost("203.0.113.10")
	p := cfg.Active()
	p.KeyPath = keyPath
	cfg.SetProfile(p)

	s, err := NewOn(app.New(cfg), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	type resp struct {
		OK      bool   `json:"ok"`
		PubKey  string `json:"pubKey"`
		Own     bool   `json:"own"`
		Payload string `json:"payload"`
		QrPng   string `json:"qrPng"`
		Error   string `json:"error"`
	}

	call := func(mode string) resp {
		body := `{"id":"` + p.ID + `","mode":"` + mode + `"}`
		rec := httptest.NewRecorder()
		s.handleAndroidKey(rec, httptest.NewRequest(http.MethodPost, "/api/androidkey", strings.NewReader(body)))
		var r resp
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatalf("ответ на mode=%q не разобрался как JSON: %v (%s)", mode, err, rec.Body.String())
		}
		if r.Error != "" {
			t.Fatalf("mode=%q вернул ошибку: %s", mode, r.Error)
		}
		return r
	}

	local := call("local")
	if local.Own {
		t.Error(`mode=local должен вернуть own=false`)
	}
	if local.PubKey != "" {
		t.Error(`mode=local не должен создавать новый ключ (pubKey не пуст)`)
	}
	var localDoc share.Doc
	if err := json.Unmarshal([]byte(local.Payload), &localDoc); err != nil {
		t.Fatalf("payload для mode=local не валидный JSON: %v", err)
	}
	if localDoc.KeyContents != keyBody {
		t.Errorf("mode=local должен положить в payload содержимое keyPath, получили:\n%q\nхотели:\n%q",
			localDoc.KeyContents, keyBody)
	}

	own := call("own")
	if !own.Own {
		t.Error(`mode=own должен вернуть own=true`)
	}
	if own.PubKey == "" {
		t.Error(`mode=own должен создать новый ключ и вернуть pubKey`)
	}
	var ownDoc share.Doc
	if err := json.Unmarshal([]byte(own.Payload), &ownDoc); err != nil {
		t.Fatalf("payload для mode=own не валидный JSON: %v", err)
	}
	if ownDoc.KeyContents == keyBody || ownDoc.KeyContents == "" {
		t.Error("mode=own должен создать новый ключ, а не переиспользовать ключ компьютера")
	}
}
