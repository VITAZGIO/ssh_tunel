package panel

// Права доступа устройств в сети устройств — как группы и политики в
// NetBird. Каждый клиент панели (SSH-пользователь tun_*) состоит в группах,
// правила говорят «группа → группа, порты». Хранится здесь, в настройках
// панели (root, 0600), а проверяет meshd: панель отдаёт ему готовую
// политику по admin-сокету, и он сам её сохраняет — переживает перезапуск
// без панели.
//
// Устройство получает права своего SSH-пользователя: meshd узнаёт его по
// владельцу сокета, а не со слов устройства, — подделать нельзя без ключа.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"

	"sshtunnel/internal/meshsvc"
)

// Особые группы meshd: все устройства и серверы, только серверы.
const (
	GroupAll     = "*"
	GroupServers = "@servers"
)

// AccessSettings — права доступа в настройках панели.
type AccessSettings struct {
	// Enabled — правила действуют. Выключено — все видят всех, как раньше.
	Enabled bool `json:"enabled,omitempty"`
	// Groups — заведённые группы (чтобы пустая группа не терялась).
	Groups []string     `json:"groups,omitempty"`
	Rules  []AccessRule `json:"rules,omitempty"`
}

// AccessRule — устройства группы From могут открывать соединения к
// устройствам группы To на порты Ports ("22,80,8000-8100"; пусто — все).
type AccessRule struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Ports string `json:"ports,omitempty"`
}

const (
	maxAccessGroups = 64
	maxAccessRules  = 256
)

func cleanGroup(g string) (string, error) {
	g = strings.TrimSpace(g)
	if g == "" || len([]rune(g)) > 40 {
		return "", errors.New("имя группы — от 1 до 40 символов")
	}
	for _, r := range g {
		if unicode.IsControl(r) {
			return "", errors.New("недопустимый символ в имени группы")
		}
	}
	return g, nil
}

// cleanGroups — список групп без повторов и особых имён.
func cleanGroups(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, g := range in {
		g, err := cleanGroup(g)
		if err != nil {
			return nil, err
		}
		if g == GroupAll || g == GroupServers {
			return nil, errors.New("группы «*» и «@servers» особые — в них не вступают")
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	if len(out) > maxAccessGroups {
		return nil, errors.New("слишком много групп")
	}
	sort.Strings(out)
	return out, nil
}

func (a *AccessSettings) clean() error {
	groups, err := cleanGroups(a.Groups)
	if err != nil {
		return err
	}
	a.Groups = groups
	if len(a.Rules) > maxAccessRules {
		return errors.New("слишком много правил")
	}
	known := map[string]bool{GroupAll: true, GroupServers: true}
	for _, g := range groups {
		known[g] = true
	}
	for i := range a.Rules {
		r := &a.Rules[i]
		r.From, r.To = strings.TrimSpace(r.From), strings.TrimSpace(r.To)
		r.Ports = strings.Join(strings.Fields(strings.ReplaceAll(r.Ports, " ", "")), "")
		if !known[r.From] || !known[r.To] {
			return errors.New("в правиле неизвестная группа: " + r.From + " → " + r.To)
		}
		if r.From == GroupServers {
			return errors.New("серверы сами соединения не открывают — их можно указать только как цель")
		}
		if err := meshsvc.ValidPorts(r.Ports); err != nil {
			return err
		}
	}
	return nil
}

// buildPolicy — политика для meshd из настроек и списка клиентов.
func buildPolicy(a AccessSettings, clients []Client) meshsvc.Policy {
	p := meshsvc.Policy{Enabled: a.Enabled, Users: map[string][]string{}}
	for _, c := range clients {
		if len(c.Groups) > 0 && c.Username != "" {
			p.Users[c.Username] = append([]string(nil), c.Groups...)
		}
	}
	for _, r := range a.Rules {
		p.Rules = append(p.Rules, meshsvc.PolicyRule{From: r.From, To: r.To, Ports: r.Ports})
	}
	return p
}

// pushPolicy отдаёт meshd свежую политику. meshd здесь нет (сервер не
// главный или сеть устройств выключена) — не ошибка: отдавать некому.
func (s *Server) pushPolicy(ctx context.Context) error {
	if s.mesh == nil || s.settings == nil {
		return nil
	}
	if s.mesh.settings.Get().Mesh.Role != MeshRoleMain && !s.mesh.sys.UnitState(meshdUnit).Active {
		return nil
	}
	p := buildPolicy(s.settings.Get().Access, s.clients.List())
	return s.mesh.admin.SetPolicy(ctx, p)
}

// SyncAccess — при старте панели: meshd мог поставиться или обновиться без
// неё. Повторяем, пока meshd не ответит (он мог ещё не подняться).
func (s *Server) SyncAccess() {
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.pushPolicy(ctx)
		cancel()
		if err == nil {
			return
		}
		if i == 0 {
			log.Printf("права доступа: meshd пока не принял политику (%v) — повторю", err)
		}
		time.Sleep(10 * time.Second)
	}
}

// handleAccess: GET — настройки прав и клиенты с группами; POST — сохранить
// настройки (группы, правила, вкл/выкл).
func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "settings_unavailable"})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"access": s.settings.Get().Access})
		return
	}
	var req AccessSettings
	if !decodePost(w, r, &req) {
		return
	}
	if err := req.clean(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Группа, в которой ещё состоят клиенты, не пропадает из списка молча.
	have := map[string]bool{}
	for _, g := range req.Groups {
		have[g] = true
	}
	for _, c := range s.clients.List() {
		for _, g := range c.Groups {
			if !have[g] {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "в группе «" + g + "» ещё есть устройства (" + c.Name + ") — сначала убери их из неё"})
				return
			}
		}
	}
	if err := s.settings.Update(func(st *Settings) error { st.Access = req; return nil }); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.respondPushed(w, r)
}

// respondPushed отдаёт политику meshd и отвечает странице: сохранено
// всегда, а не дошло до meshd — предупреждение (он применит при старте
// панели, см. SyncAccess).
func (s *Server) respondPushed(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"ok": true}
	if err := s.pushPolicy(r.Context()); err != nil {
		resp["warning"] = "сохранено, но meshd не принял: " + err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleClientGroups — группы одного клиента.
func (s *Server) handleClientGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string   `json:"id"`
		Groups []string `json:"groups"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	groups, err := cleanGroups(req.Groups)
	if err == nil && s.settings != nil {
		known := map[string]bool{}
		for _, g := range s.settings.Get().Access.Groups {
			known[g] = true
		}
		for _, g := range groups {
			if !known[g] {
				err = errors.New("нет такой группы: " + g)
			}
		}
	}
	if err == nil {
		err = s.clients.Update(req.ID, func(c *Client) { c.Groups = groups })
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.respondPushed(w, r)
}

// minCreatorPassword — пароль создателя защищает от того, кто унёс
// устройство или его файл настроек: короткий подобрать проще.
const minCreatorPassword = 8

// handleClientCreator — включить или выключить клиенту право заводить
// новых клиентов. Включение требует пароль; пустой пароль при уже
// включённом праве — оставить прежний.
func (s *Server) handleClientCreator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		CanCreate bool   `json:"canCreate"`
		Password  string `json:"password"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	var hash string
	if req.Password != "" {
		if len([]rune(req.Password)) < minCreatorPassword {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пароль — не короче 8 символов"})
			return
		}
		h, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		hash = string(h)
	}
	err := s.clients.Update(req.ID, func(c *Client) {
		c.CanCreate = req.CanCreate
		if hash != "" {
			c.CreatorHash = hash
		}
		if !req.CanCreate {
			c.CreatorHash = ""
		}
	})
	if err == nil && req.CanCreate {
		if c, _ := s.clients.Get(req.ID); c.CreatorHash == "" {
			s.clients.Update(req.ID, func(c *Client) { c.CanCreate = false })
			err = errors.New("задай пароль, по которому он будет создавать пользователей")
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
