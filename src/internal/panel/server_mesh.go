package panel

import (
	"encoding/json"
	"io"
	"net/http"
)

// WithSettings подключает настройки панели (ёмкость сервера и прочее).
func (s *Server) WithSettings(st *SettingsStore) *Server {
	s.settings = st
	return s
}

// WithMesh подключает сеть устройств.
func (s *Server) WithMesh(m *MeshManager) *Server {
	s.mesh = m
	return s
}

func (s *Server) meshRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/settings/capacity", s.requireAuth(s.handleCapacity))
	mux.HandleFunc("/api/mesh/state", s.requireAuth(s.withMesh(s.handleMeshState)))
	mux.HandleFunc("/api/mesh/role", s.requireAuth(s.withMesh(s.handleMeshRole)))
	mux.HandleFunc("/api/mesh/reinstall", s.requireAuth(s.withMesh(s.handleMeshReinstall)))
	mux.HandleFunc("/api/mesh/rename", s.requireAuth(s.withMesh(s.handleMeshRename)))
	mux.HandleFunc("/api/mesh/forget", s.requireAuth(s.withMesh(s.handleMeshForget)))
	mux.HandleFunc("/api/mesh/links/create", s.requireAuth(s.withMesh(s.handleMeshLinkCreate)))
	mux.HandleFunc("/api/mesh/links/delete", s.requireAuth(s.withMesh(s.handleMeshLinkDelete)))
	mux.HandleFunc("/api/mesh/join", s.requireAuth(s.withMesh(s.handleMeshJoin)))
}

func (s *Server) withMesh(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.mesh == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "mesh_unavailable"})
			return
		}
		next(w, r)
	}
}

// decodePost читает тело POST-запроса в v; иначе отвечает ошибкой сам.
func decodePost(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return false
	}
	return true
}

func replyErr(w http.ResponseWriter, err error, ok any) {
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// handleCapacity — ёмкость сервера: сколько устройств держать одновременно
// (см. capacity.go).
func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	current := 0
	if s.settings != nil {
		current = s.settings.Get().MaxDevices
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, Capacity(current))
		return
	}
	var req struct {
		MaxDevices int `json:"maxDevices"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	if s.settings == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "settings_unavailable"})
		return
	}
	if err := ValidMaxDevices(req.MaxDevices); err != nil {
		replyErr(w, err, nil)
		return
	}
	// Сначала sshd, потом настройка: не применилось — в настройках остаётся
	// то, что действует на самом деле.
	if err := ApplyCapacity(req.MaxDevices); err != nil {
		replyErr(w, err, nil)
		return
	}
	err := s.settings.Update(func(st *Settings) error {
		st.MaxDevices = req.MaxDevices
		return nil
	})
	replyErr(w, err, Capacity(req.MaxDevices))
}

func (s *Server) handleMeshState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mesh.State(r.Context()))
}

func (s *Server) handleMeshRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
		Name string `json:"name"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	var err error
	switch req.Role {
	case MeshRoleMain:
		err = s.mesh.SetMain(req.Name)
	case "off":
		err = s.mesh.Disable()
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_role"})
		return
	}
	replyErr(w, err, map[string]bool{"ok": true})
}

func (s *Server) handleMeshReinstall(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.Reinstall(), map[string]bool{"ok": true})
}

type meshDeviceReq struct {
	Net    string `json:"net"`
	Device string `json:"device"`
	Alias  string `json:"alias"`
}

func (s *Server) handleMeshRename(w http.ResponseWriter, r *http.Request) {
	var req meshDeviceReq
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.Rename(r.Context(), req.Net, req.Device, req.Alias), map[string]bool{"ok": true})
}

func (s *Server) handleMeshForget(w http.ResponseWriter, r *http.Request) {
	var req meshDeviceReq
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.Forget(r.Context(), req.Net, req.Device), map[string]bool{"ok": true})
}

func (s *Server) handleMeshLinkCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	link, code, err := s.mesh.CreateLink(req.Name, req.Host, req.Port)
	replyErr(w, err, map[string]any{"ok": true, "link": link, "code": code})
}

func (s *Server) handleMeshLinkDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.DeleteLink(req.ID), map[string]bool{"ok": true})
}

func (s *Server) handleMeshJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		Host string `json:"host"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	replyErr(w, s.mesh.Join(req.Code, req.Host), map[string]bool{"ok": true})
}
