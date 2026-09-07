package webui

import (
	"errors"
	"net/http"
	"path/filepath"

	"sshtunnel/internal/updater"
)

// handleUpdateCheck ходит на страницу релизов GitHub и сравнивает версии.
// Запрос делает программа, а не браузер: страница открыта по локальному
// адресу, и обращение к чужому домену из неё упёрлось бы в CORS.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	res, err := updater.Check(r.Context())
	if err != nil {
		code := "update_failed"
		if errors.Is(err, updater.ErrRateLimited) {
			code = "update_rate_limited"
		}
		writeJSON(w, map[string]any{"error": err.Error(), "code": code})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "result": res})
}

// handleUpdateDownload скачивает файл новой версии в папку загрузок. Сама
// программа себя не подменяет (см. docs/UPDATE_SPEC.md): на Windows файл
// занят собственным процессом, на Linux лежит в системной папке и требует
// root. Если скачать не вышло, интерфейс предлагает открыть страницу релиза.
func (s *Server) handleUpdateDownload(w http.ResponseWriter, r *http.Request) {
	res, err := updater.Check(r.Context())
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	if res.AssetURL == "" {
		writeJSON(w, map[string]any{
			"error": "у релиза нет файла для этой системы", "url": res.URL,
		})
		return
	}
	path, err := updater.Download(r.Context(), res.AssetURL, res.AssetName)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error(), "url": res.URL})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "path": path, "dir": filepath.Dir(path), "name": filepath.Base(path),
	})
}
