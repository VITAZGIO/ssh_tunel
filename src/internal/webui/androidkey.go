// Вкладка «Android» в мастере настройки: телефону нужен ключ, которым можно
// подключиться к серверу. Два варианта: взять ключ этого компьютера (он уже
// разрешён на сервере — команду для authorized_keys показывать не нужно) или
// сделать для телефона отдельный ключ (прежнее поведение — новая пара, и
// команду для сервера придётся выполнить под root). Ключ отдаётся сразу в
// формате exportDoc (том же, что у экспорта сервера файлом), чтобы будущее
// приложение на телефоне могло разобрать его тем же кодом, что уже умеет
// читать этот формат — здесь он просто уходит через QR вместо файла.
package webui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"

	qrcode "github.com/skip2/go-qrcode"

	"sshtunnel/internal/share"
)

func (s *Server) handleAndroidKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]string{"error": "не разобрал запрос: " + err.Error()})
		return
	}
	p, ok := s.app.Config().ProfileByID(req.ID)
	if !ok {
		writeJSON(w, map[string]string{"error": "сервер не найден"})
		return
	}

	own := req.Mode == "own"
	var pub, priv string
	if own {
		var err error
		pub, priv, err = generateKeyPair()
		if err != nil {
			writeJSON(w, map[string]string{"error": "не удалось создать ключ: " + err.Error()})
			return
		}
	} else {
		data, err := os.ReadFile(p.KeyPath)
		if err != nil {
			writeJSON(w, map[string]string{"error": "не могу прочитать ключ этого компьютера: " + err.Error()})
			return
		}
		priv = string(data)
	}

	doc := share.Doc{
		Name: p.Name, Flag: p.Flag, Host: p.Host, SSHPort: p.SSHPort, User: p.User,
		SocksPort: p.SocksPort, HTTPPort: p.HTTPPort, PoolSize: p.PoolSize,
		FilterMode: p.FilterMode, FilterApps: p.FilterApps, DirectHosts: p.DirectHosts,
		LocalViaTunnel: p.LocalViaTunnel, KeyIncluded: true, KeyContents: priv,
	}
	payload, err := share.Build(doc)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	png, err := qrcode.Encode(string(payload), qrcode.Low, 320)
	if err != nil {
		writeJSON(w, map[string]string{"error": "не удалось собрать QR-код: " + err.Error()})
		return
	}

	writeJSON(w, map[string]any{
		"ok": true, "host": p.Host, "sshPort": p.SSHPort, "user": p.User,
		"pubKey": pub, "own": own, "payload": string(payload),
		"qrPng": base64.StdEncoding.EncodeToString(png),
	})
}
