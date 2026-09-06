// Экспорт и импорт одного сервера файлом — чтобы поделиться готовым
// подключением с человеком, который в этом не разбирается: он получает файл,
// нажимает «Импорт», и у него уже есть сервер, к которому можно подключаться.
//
// Файл — обычный JSON, без шифрования. Осознанный выбор: пароль на файл
// добавил бы шаг «сообщи ему пароль отдельно», а весь смысл в том, чтобы
// человеку хватило одного клика. Плата за простоту — если ключ включён в
// файл (по умолчанию так), файл целиком равносилен паролю от сервера: его
// нельзя пересылать через что попало и надо удалить после использования.
// Об этом прямо предупреждает страница перед экспортом.
package webui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"sshtunnel/internal/config"
	"sshtunnel/internal/share"
)

// handleProfileExport отдаёт сервер файлом: сама программа его не сохраняет и
// не отправляет — собирает JSON и возвращает странице, а та превращает его в
// скачивание через Blob. Так эндпоинт остаётся под тем же токеном, что и
// весь остальной API, а не отдельной ссылкой без защиты.
func (s *Server) handleProfileExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID         string `json:"id"`
		IncludeKey bool   `json:"includeKey"`
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

	doc := share.Doc{
		Name: p.Name, Flag: p.Flag, Host: p.Host, SSHPort: p.SSHPort, User: p.User,
		SocksPort: p.SocksPort, HTTPPort: p.HTTPPort, PoolSize: p.PoolSize,
		FilterMode: p.FilterMode, FilterApps: p.FilterApps, DirectHosts: p.DirectHosts,
		LocalViaTunnel: p.LocalViaTunnel,
		Panel:          p.Panel, ClientID: p.ClientID, DeviceName: p.DeviceName,
	}
	if req.IncludeKey {
		data, err := os.ReadFile(p.KeyPath)
		if err != nil {
			writeJSON(w, map[string]string{"error": "не могу прочитать приватный ключ: " + err.Error()})
			return
		}
		doc.KeyIncluded = true
		doc.KeyContents = string(data)
	}

	pretty, err := share.Build(doc)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "filename": exportFilename(p.Name), "data": string(pretty)})
}

// exportFilename — читаемое имя файла из названия сервера: только
// латиница/цифры, остальное заменяется на дефис, чтобы не спотыкаться о
// пробелы, кириллицу и слэши в разных ОС.
func exportFilename(name string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, name)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "server"
	}
	return "ssh_tunnel-" + slug + ".json"
}

// handleProfileImport создаёт новый сервер из экспортированного файла и сразу
// делает его активным — цель ровно в том, чтобы после импорта оставалось
// только нажать «Подключить», без дополнительных решений.
func (s *Server) handleProfileImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, map[string]string{"error": "не удалось прочитать файл: " + err.Error()})
		return
	}
	doc, err := share.Parse(body)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}

	p, err := s.app.AddProfile(doc.Name, doc.Flag)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	p.Host, p.SSHPort, p.User = doc.Host, doc.SSHPort, doc.User
	p.SocksPort, p.HTTPPort, p.PoolSize = doc.SocksPort, doc.HTTPPort, doc.PoolSize
	p.FilterMode, p.FilterApps = doc.FilterMode, doc.FilterApps
	p.DirectHosts, p.LocalViaTunnel = doc.DirectHosts, doc.LocalViaTunnel
	p.Panel, p.ClientID, p.DeviceName = doc.Panel, doc.ClientID, doc.DeviceName

	keyImported := false
	if doc.KeyIncluded && strings.TrimSpace(doc.KeyContents) != "" {
		path, err := saveImportedKey(p.ID, doc.KeyContents)
		if err != nil {
			writeJSON(w, map[string]string{"error": "не удалось сохранить ключ: " + err.Error()})
			return
		}
		p.KeyPath = path
		keyImported = true
	}

	if err := s.app.UpdateProfile(p); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	// Активным — отдельным шагом, после того как профиль уже на диске:
	// SwitchProfile сверяется со списком серверов и не найдёт наш, если
	// вызвать его раньше сохранения.
	note, err := s.app.SwitchProfile(p.ID)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "profile": p, "keyImported": keyImported, "note": note, "config": s.app.Config(),
	})
}

// saveImportedKey кладёт присланный ключ в свою папку рядом с настройками —
// не в ~/.ssh: незачем засорять чужую папку ключей и рисковать что-то в ней
// перезаписать. Имя файла завязано на id профиля, так что у каждого
// импортированного сервера свой ключ, даже если их несколько.
func saveImportedKey(profileID, contents string) (string, error) {
	if strings.TrimSpace(profileID) == "" {
		return "", errors.New("пустой id профиля")
	}
	dir := filepath.Join(config.Dir(), "imported_keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, profileID+"_id_ed25519")
	body := strings.TrimRight(contents, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
