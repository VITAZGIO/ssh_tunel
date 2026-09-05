// Вкладка «Android» в мастере настройки: у телефона должен быть свой ключ,
// отдельный от того, что лежит на компьютере — компрометация одного не
// должна тянуть за собой второй. Ключ отдаётся один раз, сразу в формате
// exportDoc (том же, что у экспорта сервера файлом), чтобы будущее
// приложение на телефоне могло разобрать его тем же кодом, что уже умеет
// читать этот формат — здесь он просто уходит через QR вместо файла.
package webui

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	qrcode "github.com/skip2/go-qrcode"
)

func (s *Server) handleAndroidKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
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

	pub, priv, err := generateKeyPair()
	if err != nil {
		writeJSON(w, map[string]string{"error": "не удалось создать ключ: " + err.Error()})
		return
	}

	doc := exportDoc{
		Format: 1, Name: p.Name, Flag: p.Flag, Host: p.Host, SSHPort: p.SSHPort, User: p.User,
		SocksPort: p.SocksPort, HTTPPort: p.HTTPPort, PoolSize: p.PoolSize,
		FilterMode: p.FilterMode, FilterApps: p.FilterApps, DirectHosts: p.DirectHosts,
		LocalViaTunnel: p.LocalViaTunnel, KeyIncluded: true, KeyContents: priv,
	}
	payload, err := json.Marshal(doc)
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
		"pubKey": pub, "payload": string(payload),
		"qrPng": base64.StdEncoding.EncodeToString(png),
	})
}
