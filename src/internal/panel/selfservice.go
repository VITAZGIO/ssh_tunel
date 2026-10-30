package panel

// Создание пользователей из программы (мастер настройки) — без входа в
// панель. Отдельный вход на 127.0.0.1:47832, только для клиентов туннеля:
// снаружи сервера его нет, а клиент достаёт до него через свой SSH-проброс
// (брандмауэр туннеля пускает на 127.0.0.1 только порты 47830–47832).
//
// Кто пришёл, узнаём не со слов программы, а по владельцу сокета: проброс
// открывает процесс sshd, работающий от имени пользователя туннеля, и ядро
// пишет его uid в /proc/net/tcp. Значит, звонящий уже прошёл SSH со своим
// ключом. Вторым шагом — пароль, который владелец задал этому клиенту в
// панели вместе с галкой «может создавать пользователей»: ключ мог уйти
// вместе с украденным устройством, пароль — нет.
//
// Что можно: завести обычного клиента с группами создателя (не больше) и
// без права создавать. Панелью, правами и чужими клиентами отсюда не
// управлять.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SelfServiceAddr — где слушает вход для создания пользователей.
const SelfServiceAddr = "127.0.0.1:47832"

// maxCreatedPerCreator — сколько клиентов один создатель может завести
// (удалённые не считаются). Больше — через панель.
const maxCreatedPerCreator = 10

// procTCPUID — uid владельца сокета на той стороне соединения r.
func procTCPUID(r *http.Request) (int, bool) {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return 0, false
	}
	la, err1 := net.ResolveTCPAddr("tcp", local.String())
	ra, err2 := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	data, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return 0, false
	}
	return socketUID(string(data), ra, la)
}

// socketUID ищет в таблице /proc/net/tcp сокет с локальным адресом local и
// удалённым remote и возвращает uid его владельца.
func socketUID(table string, local, remote *net.TCPAddr) (int, bool) {
	hexAddr := func(a *net.TCPAddr) string {
		ip := a.IP.To4()
		if ip == nil {
			return ""
		}
		return strings.ToUpper(strconv.FormatUint(uint64(ip[3])<<24|uint64(ip[2])<<16|uint64(ip[1])<<8|uint64(ip[0]), 16)) +
			":" + strings.ToUpper(strconv.FormatUint(uint64(a.Port), 16))
	}
	pad := func(h string) string {
		host, port, _ := strings.Cut(h, ":")
		return strings.Repeat("0", 8-len(host)) + host + ":" + strings.Repeat("0", 4-len(port)) + port
	}
	l, r := hexAddr(local), hexAddr(remote)
	if l == "" || r == "" {
		return 0, false
	}
	l, r = pad(l), pad(r)
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) > 7 && f[1] == l && f[2] == r {
			uid, err := strconv.Atoi(f[7])
			return uid, err == nil
		}
	}
	return 0, false
}

// callerClient — клиент панели, от имени которого пришёл запрос.
func (s *Server) callerClient(r *http.Request) (Client, bool) {
	find := s.connUID
	if find == nil {
		find = procTCPUID
	}
	uid, ok := find(r)
	if !ok {
		return Client{}, false
	}
	for _, c := range s.clients.List() {
		if c.UID == uid && c.UID != 0 && c.State == StateActive {
			full, ok := s.clients.Get(c.ID)
			return full, ok
		}
	}
	return Client{}, false
}

// SelfServiceHandler — вход для программ клиентов (см. начало файла).
func (s *Server) SelfServiceHandler() http.Handler {
	mux := http.NewServeMux()
	// Только JSON: браузер не пошлёт его на чужой адрес без спроса (CORS),
	// так что страница в интернете не сможет дёрнуть этот вход за человека.
	post := func(w http.ResponseWriter, r *http.Request, v any) bool {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "нужен POST с JSON"})
			return false
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(v); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный запрос"})
			return false
		}
		return true
	}
	mux.HandleFunc("/v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !post(w, r, &req) {
			return
		}
		c, ok := s.callerClient(r)
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "этот сервер тебя не знает как клиента панели"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": c.Name, "canCreate": c.CanCreate && c.CreatorHash != ""})
	})
	mux.HandleFunc("/v1/create", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Password   string `json:"password"`
			Name       string `json:"name"`
			DeviceType string `json:"deviceType"`
		}
		if !post(w, r, &req) {
			return
		}
		c, err := s.selfCreate(r, req.Password, req.Name, DeviceType(req.DeviceType))
		if err != nil {
			code := http.StatusForbidden
			if errors.Is(err, errSelfBadInput) {
				code = http.StatusBadRequest
			}
			writeJSON(w, code, map[string]string{"error": strings.TrimPrefix(err.Error(), errSelfBadInput.Error()+": ")})
			return
		}
		payload, err := BuildClientConfigPayload(c, s.sshHost, s.sshPort, s.panelURL)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": c.ID, "name": c.Name, "config": payload.JSON})
	})
	return mux
}

var errSelfBadInput = errors.New("неверные данные")

func (s *Server) selfCreate(r *http.Request, password, name string, dt DeviceType) (Client, error) {
	creator, ok := s.callerClient(r)
	if !ok {
		return Client{}, errors.New("этот сервер тебя не знает как клиента панели")
	}
	if !creator.CanCreate || creator.CreatorHash == "" {
		return Client{}, errors.New("тебе не разрешено создавать пользователей — это включается в панели сервера")
	}
	key := "creator:" + creator.ID
	if locked, left := s.limiter.Before(key); locked {
		return Client{}, errors.New("слишком много неверных паролей, попробуй через " + left.Round(time.Second).String())
	}
	if bcrypt.CompareHashAndPassword([]byte(creator.CreatorHash), []byte(password)) != nil {
		s.limiter.Fail(key)
		log.Printf("создание пользователя: неверный пароль от %s (%s)", creator.Name, creator.ID)
		return Client{}, errors.New("неверный пароль")
	}
	s.limiter.Success(key)
	if dt == "" {
		dt = DeviceWindows
	}
	made := 0
	for _, c := range s.clients.List() {
		if c.CreatedBy == creator.ID {
			made++
		}
	}
	if made >= maxCreatedPerCreator {
		return Client{}, errors.New("ты уже завёл " + strconv.Itoa(made) + " пользователей — больше можно только в панели")
	}
	c, err := s.clients.CreateClientBy(name, dt, creator)
	if err != nil {
		return Client{}, fmt.Errorf("%w: %v", errSelfBadInput, err)
	}
	log.Printf("создание пользователя: %s (%s) завёл %s (%s)", creator.Name, creator.ID, c.Name, c.ID)
	if len(c.Groups) > 0 {
		if err := s.pushPolicy(r.Context()); err != nil {
			log.Printf("права доступа: meshd не принял политику: %v", err)
		}
	}
	return c, nil
}
