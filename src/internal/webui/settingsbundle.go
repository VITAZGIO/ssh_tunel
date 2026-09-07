package webui

import (
	"io"
	"net/http"
	"os"
	"strings"

	"sshtunnel/internal/config"
	"sshtunnel/internal/share"
)

// handleSettingsExport отдаёт ВСЕ настройки одним текстом: список серверов
// вместе с ключами и общие галочки. Сделано ради переноса на другое
// устройство одним сообщением в мессенджере — человеку не нужно повторять
// настройку по пунктам на каждом компьютере в доме.
//
// Ключи входят в выгрузку всегда: без них на новом устройстве не подключиться,
// а отдельная галочка «с ключами или без» на этом экране означала бы, что
// половина выгрузок приезжает нерабочими. Отсюда прямое следствие: этот текст
// — такой же секрет, как сам ключ, и интерфейс об этом предупреждает.
func (s *Server) handleSettingsExport(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()
	b := share.Bundle{
		Active:           -1,
		Language:         cfg.Language,
		Verbose:          cfg.Verbose,
		AutoStart:        cfg.AutoStart,
		AutoConnect:      cfg.AutoConnect,
		ShowServerPicker: cfg.ShowServerPicker,
		AutoPickFastest:  cfg.AutoPickFastest,
		SysProxy:         cfg.SysProxy,
		SetEnvVars:       cfg.SetEnvVars,
	}
	for i, p := range cfg.Profiles {
		doc := share.Doc{
			Name: p.Name, Flag: p.Flag, Host: p.Host, SSHPort: p.SSHPort, User: p.User,
			SocksPort: p.SocksPort, HTTPPort: p.HTTPPort, PoolSize: p.PoolSize,
			FilterMode: p.FilterMode, FilterApps: p.FilterApps, DirectHosts: p.DirectHosts,
			LocalViaTunnel: p.LocalViaTunnel,
			Panel:          p.Panel, ClientID: p.ClientID, DeviceName: p.DeviceName,
		}
		// Нечитаемый ключ не повод оборвать всю выгрузку: остальные серверы
		// перенести всё равно полезно, а про этот интерфейс скажет отдельно.
		if data, err := os.ReadFile(p.KeyPath); err == nil {
			doc.KeyIncluded = true
			doc.KeyContents = string(data)
		}
		if p.ID == cfg.ActiveProfile {
			b.Active = i
		}
		b.Servers = append(b.Servers, doc)
	}

	data, err := share.BuildBundle(b)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	missing := 0
	for _, d := range b.Servers {
		if !d.KeyIncluded {
			missing++
		}
	}
	writeJSON(w, map[string]any{
		"ok": true, "data": string(data), "servers": len(b.Servers), "keysMissing": missing,
	})
}

// handleSettingsImport заменяет все настройки присланной выгрузкой. Именно
// заменяет, а не добавляет: смысл кнопки — «сделай у меня так же, как у
// тебя», и дописывание чужих серверов к своим давало бы список, в котором
// человек не разберётся.
func (s *Server) handleSettingsImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSON(w, map[string]string{"error": "не удалось прочитать настройки: " + err.Error()})
		return
	}
	b, err := share.ParseBundle(body)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}

	// Туннель остановить надо до подмены списка серверов: он поднят по
	// адресу, которого после импорта в настройках может уже не быть.
	s.app.Stop()

	cfg := s.app.Config()
	cfg.Profiles = nil
	cfg.Language = b.Language
	cfg.Verbose = b.Verbose
	cfg.AutoStart = b.AutoStart
	cfg.AutoConnect = b.AutoConnect
	cfg.ShowServerPicker = b.ShowServerPicker
	cfg.AutoPickFastest = b.AutoPickFastest
	cfg.SysProxy = b.SysProxy
	cfg.SetEnvVars = b.SetEnvVars
	cfg.ActiveProfile = ""

	keysMissing := 0
	for i, doc := range b.Servers {
		p := config.NewProfile(doc.Name, doc.Flag)
		p.Host, p.SSHPort, p.User = doc.Host, doc.SSHPort, doc.User
		p.SocksPort, p.HTTPPort, p.PoolSize = doc.SocksPort, doc.HTTPPort, doc.PoolSize
		p.FilterMode, p.FilterApps = doc.FilterMode, doc.FilterApps
		p.DirectHosts, p.LocalViaTunnel = doc.DirectHosts, doc.LocalViaTunnel
		p.Panel, p.ClientID, p.DeviceName = doc.Panel, doc.ClientID, doc.DeviceName
		if doc.KeyIncluded && strings.TrimSpace(doc.KeyContents) != "" {
			path, err := saveImportedKey(p.ID, doc.KeyContents)
			if err != nil {
				writeJSON(w, map[string]string{"error": "не удалось сохранить ключ: " + err.Error()})
				return
			}
			p.KeyPath = path
		} else {
			keysMissing++
		}
		cfg.Profiles = append(cfg.Profiles, p)
		if i == b.Active {
			cfg.ActiveProfile = p.ID
		}
	}
	if cfg.ActiveProfile == "" && len(cfg.Profiles) > 0 {
		cfg.ActiveProfile = cfg.Profiles[0].ID
	}

	if _, err := s.app.SetConfig(cfg); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "servers": len(cfg.Profiles), "keysMissing": keysMissing,
		"mobileOnly": len(b.Mobile) > 0, "config": s.app.Config(),
	})
}
