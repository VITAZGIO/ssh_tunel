package mesh

// Вход meshd для панели на сервере: характеристики устройств, задержка,
// трафик, переименование и удаление. Настоящий бинарник, настоящий
// unix-сокет.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

type adminDev struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Alias       string   `json:"alias"`
	Display     string   `json:"display"`
	Host        string   `json:"host"`
	Online      bool     `json:"online"`
	Platform    string   `json:"platform"`
	App         string   `json:"app"`
	Mode        string   `json:"mode"`
	Via         string   `json:"via"`
	RTTMs       float64  `json:"rttMs"`
	Quality     string   `json:"quality"`
	BytesUp     int64    `json:"bytesUp"`
	BytesDown   int64    `json:"bytesDown"`
	Sessions    int64    `json:"sessions"`
	ConnectedAt int64    `json:"connectedAt"`
	NAT         *NATInfo `json:"nat"`
	Direct      []struct {
		IP    string  `json:"ip"`
		RTTMs float64 `json:"rttMs"`
	} `json:"direct"`
}

type adminStateResp struct {
	Version  string `json:"version"`
	Networks []struct {
		ID      string     `json:"id"`
		Devices []adminDev `json:"devices"`
	} `json:"networks"`
}

func adminClient(sock string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

func getState(t *testing.T, c *http.Client) adminStateResp {
	t.Helper()
	resp, err := c.Get("http://meshd/v1/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st adminStateResp
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func post(t *testing.T, c *http.Client, path string, body any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.Post("http://meshd"+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: код %d", path, resp.StatusCode)
	}
}

func findDev(st adminStateResp, name string) (string, adminDev, bool) {
	for _, n := range st.Networks {
		for _, d := range n.Devices {
			if d.Name == name {
				return n.ID, d, true
			}
		}
	}
	return "", adminDev{}, false
}

func TestПанельВидитУстройстваИКачествоСвязи(t *testing.T) {
	addr, sock := startMeshdAdmin(t)
	key := NewKey()
	laptop := startClient(t, addr, Config{Key: key, Name: "laptop", AllowIncoming: true,
		Platform: "windows", AppVersion: "v9.9.9", Mode: "vpn", Via: "de.example.com"})
	startClient(t, addr, Config{Key: key, Name: "server", AllowIncoming: true,
		Platform: "linux", Mode: "proxy", Via: "nl.example.com"})
	waitFor(t, func() bool { _, ok := laptop.Resolve("server"); return ok }, "список не дошёл")

	admin := adminClient(sock)
	st := getState(t, admin)
	if st.Version == "" || len(st.Networks) != 1 || len(st.Networks[0].Devices) != 2 {
		t.Fatalf("состояние: %+v", st)
	}
	_, d, ok := findDev(st, "laptop")
	if !ok || !d.Online || d.Platform != "windows" || d.App != "v9.9.9" || d.Mode != "vpn" ||
		d.Via != "de.example.com" || d.Sessions != 1 || d.ConnectedAt == 0 {
		t.Fatalf("характеристики ноутбука: %+v", d)
	}

	// Первый замер задержки meshd делает сразу после подключения.
	waitFor(t, func() bool {
		_, d, _ := findDev(getState(t, admin), "laptop")
		return d.Quality != "unknown" && d.RTTMs > 0
	}, "задержка не измерилась")

	// Трафик через сеть устройств считается обоим.
	port := echoService(t, "server")
	conn, err := laptop.DialPeer("server.mesh:" + strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	ask(t, conn, "привет")
	waitFor(t, func() bool {
		st := getState(t, admin)
		_, l, _ := findDev(st, "laptop")
		_, s, _ := findDev(st, "server")
		return l.BytesUp > 0 && l.BytesDown > 0 && s.BytesUp > 0 && s.BytesDown > 0
	}, "трафик не посчитался")
}

func TestПанельПереименовываетИЗабывает(t *testing.T) {
	addr, sock := startMeshdAdmin(t)
	key := NewKey()
	a := startClient(t, addr, Config{Key: key, Name: "a", AllowIncoming: true})
	b := startClient(t, addr, Config{Key: key, Name: "b", AllowIncoming: true})
	waitFor(t, func() bool { _, ok := a.Resolve("b"); return ok }, "список не дошёл")

	admin := adminClient(sock)
	netID, dev, _ := findDev(getState(t, admin), "b")
	post(t, admin, "/v1/rename", map[string]string{"net": netID, "device": dev.ID, "alias": "Домашний сервер"})

	// Новое имя видят и соседи, и само устройство.
	waitFor(t, func() bool { _, ok := a.Resolve("domashniy-server.mesh"); return ok }, "сосед не увидел новое имя")
	waitFor(t, func() bool { return b.Status().Self.Host == "domashniy-server" }, "устройство не узнало своё новое имя")
	if _, d, _ := findDev(getState(t, admin), "b"); d.Display != "Домашний сервер" || d.Alias == "" {
		t.Fatalf("после переименования: %+v", d)
	}

	post(t, admin, "/v1/forget", map[string]string{"net": netID, "device": dev.ID})
	// Устройство вернётся само — но уже заново, без имени из панели.
	waitFor(t, func() bool {
		_, d, ok := findDev(getState(t, admin), "b")
		return ok && d.Alias == "" && d.Host == "b"
	}, "забытое устройство не вернулось как новое")
}
