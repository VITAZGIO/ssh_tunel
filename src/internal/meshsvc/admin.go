package meshsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Device — устройство сети глазами панели (см. adminDevice в cmd/meshd).
type Device struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Alias       string  `json:"alias,omitempty"`
	Display     string  `json:"display"`
	Host        string  `json:"host"`
	IP          string  `json:"ip"`
	Online      bool    `json:"online"`
	Platform    string  `json:"platform,omitempty"`
	OS          string  `json:"os,omitempty"`
	App         string  `json:"app,omitempty"`
	Mode        string  `json:"mode,omitempty"`
	Via         string  `json:"via,omitempty"`
	Hostname    string  `json:"hostname,omitempty"`
	FirstSeen   int64   `json:"firstSeen,omitempty"`
	LastSeen    int64   `json:"lastSeen,omitempty"`
	ConnectedAt int64   `json:"connectedAt,omitempty"`
	Sessions    int64   `json:"sessions"`
	RTTMs       float64 `json:"rttMs,omitempty"`
	RTTAvgMs    float64 `json:"rttAvgMs,omitempty"`
	JitterMs    float64 `json:"jitterMs,omitempty"`
	LossPct     float64 `json:"lossPct"`
	Quality     string  `json:"quality"`
	BytesUp     int64   `json:"bytesUp"`
	BytesDown   int64   `json:"bytesDown"`
	ActiveCalls int64   `json:"activeCalls"`
}

// Network — одна сеть (все устройства с одинаковым ключом).
type Network struct {
	ID      string   `json:"id"`
	Devices []Device `json:"devices"`
}

// Server — подключённый побочный сервер.
type Server struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	IP          string   `json:"ip,omitempty"` // адрес в сети устройств
	MeshHost    string   `json:"meshHost,omitempty"`
	Addrs       []string `json:"addrs,omitempty"`
	App         string   `json:"app,omitempty"`
	ConnectedAt int64    `json:"connectedAt"`
	RTTMs       float64  `json:"rttMs,omitempty"`
	RTTAvgMs    float64  `json:"rttAvgMs,omitempty"`
	JitterMs    float64  `json:"jitterMs,omitempty"`
	LossPct     float64  `json:"lossPct"`
	Quality     string   `json:"quality"`
}

// Self — этот сервер как узел сети устройств.
type Self struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	IP      string `json:"ip"`
	Enabled bool   `json:"enabled"`
	Ports   string `json:"ports,omitempty"`
}

// State — ответ meshd на /v1/state.
type State struct {
	Version   string    `json:"version"`
	StartedAt int64     `json:"startedAt"`
	Networks  []Network `json:"networks"`
	Servers   []Server  `json:"servers"`
	Self      Self      `json:"self"`
}

// Admin — клиент входа meshd для панели (HTTP по unix-сокету).
type Admin struct {
	http *http.Client
}

// NewAdmin — клиент для сокета sock (обычно AdminSocket).
func NewAdmin(sock string) *Admin {
	return &Admin{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}}
}

// ErrNotRunning — meshd не отвечает: не установлен, остановлен или запущен
// без входа для панели (старая версия).
var ErrNotRunning = errors.New("meshd не отвечает")

func (a *Admin) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://meshd"+path, rd)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("meshd ответил %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// State — все сети, устройства и подключённые серверы.
func (a *Admin) State(ctx context.Context) (State, error) {
	var st State
	err := a.do(ctx, http.MethodGet, "/v1/state", nil, &st)
	return st, err
}

type deviceReq struct {
	Net    string `json:"net"`
	Device string `json:"device"`
	Alias  string `json:"alias,omitempty"`
}

// Rename задаёт устройству имя; пустое — вернуть его собственное.
func (a *Admin) Rename(ctx context.Context, netID, device, alias string) error {
	return a.do(ctx, http.MethodPost, "/v1/rename", deviceReq{Net: netID, Device: device, Alias: alias}, nil)
}

// Forget убирает устройство из сети.
func (a *Admin) Forget(ctx context.Context, netID, device string) error {
	return a.do(ctx, http.MethodPost, "/v1/forget", deviceReq{Net: netID, Device: device}, nil)
}

// SetSelf — как этот сервер выглядит в сети устройств: имя, пускать ли к нему
// устройства и на какие порты ("" — на все).
func (a *Admin) SetSelf(ctx context.Context, name string, enabled bool, ports string) error {
	return a.do(ctx, http.MethodPost, "/v1/self", map[string]any{"name": name, "enabled": enabled, "ports": ports}, nil)
}

// ForgetServer забывает адрес отключённого побочного сервера.
func (a *Admin) ForgetServer(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, "/v1/forget-server", deviceReq{Device: id}, nil)
}
