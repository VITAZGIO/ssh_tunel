package main

import (
	"os"
	"os/exec"
	"time"

	"sshtunnel/internal/app"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/nativeui"
	"sshtunnel/internal/webui"
	"sshtunnel/vpn/vpnlayer"
)

// engine — ядро: туннель, адаптер VPN, сеть устройств и веб-интерфейс к ним.
// Одно и то же и в службе, и в процессе окна.
type engine struct {
	a   *app.App
	srv *webui.Server
}

func newEngine() (*engine, error) {
	cfg := config.Load()
	a := app.New(cfg)
	a.SetNetLayer(vpnlayer.New(a.Bus))
	srv, err := webui.New(a)
	if err != nil {
		return nil, err
	}
	go srv.Serve()
	return &engine{a: a, srv: srv}, nil
}

// autoConnect — галочка «Подключаться сразу при запуске». Сети при
// включении компьютера может ещё не быть — StartOnLaunchWithin подождёт её
// (limit; 0 — сколько угодно).
func (e *engine) autoConnect(delay, limit time.Duration) {
	cfg := e.a.Config()
	if !cfg.AutoStart || cfg.Active().Host == "" {
		return
	}
	go func() {
		time.Sleep(delay)
		e.a.StartOnLaunchWithin(limit)
	}()
}

// trayNames — подписи у значка в трее.
var trayNames = map[string]string{
	events.StateStopped:      "VPN выключен",
	events.StateConnecting:   "подключаюсь",
	events.StateConnected:    "VPN включён",
	events.StateReconnecting: "связь потеряна",
	events.StateError:        "ошибка",
}

func showStateInTray(a *app.App) {
	ch, _ := a.Bus.Subscribe()
	for e := range ch {
		if e.Kind != events.KindState {
			continue
		}
		if name, ok := trayNames[e.State]; ok {
			nativeui.SetStatus(name)
		}
	}
}

// startSelf запускает exe ещё раз (с теми же правами, что у этого процесса).
func startSelf(args ...string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	startExe(self, args...)
}

func startExe(exe string, args ...string) {
	cmd := exec.Command(exe, args...)
	_ = cmd.Start()
}
