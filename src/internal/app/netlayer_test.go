package app

import (
	"errors"
	"sync"
	"testing"

	"sshtunnel/internal/config"
	"sshtunnel/internal/tunnel"
)

type fakeNetLayer struct {
	mu        sync.Mutex
	prepared  int
	attached  []*tunnel.Tunnel
	detached  int
	attachErr error
}

func (f *fakeNetLayer) Prepare(cfg *tunnel.Config) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	cfg.SocksAddr, cfg.HTTPAddr = "", ""
}

func (f *fakeNetLayer) Attach(tun *tunnel.Tunnel, _ config.Profile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachErr != nil {
		return f.attachErr
	}
	f.attached = append(f.attached, tun)
	return nil
}

func (f *fakeNetLayer) Detach() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached++
}

func vpnApp(t *testing.T, layer *fakeNetLayer) *App {
	t.Helper()
	keyPath, pub := testKeyPath(t)
	addr := newFakeSSHServer(t, pub, true)
	p := profileAt(t, "main", addr, keyPath)
	cfg := config.Config{Profiles: []config.Profile{p}, ActiveProfile: p.ID, SysProxy: true}
	a := New(cfg)
	a.SetNetLayer(layer)
	t.Cleanup(a.StopNow)
	return a
}

// В режиме VPN системный прокси не трогается, адаптер поднимается на
// подключённом туннеле, а «Отключить» снимает его сразу, без слива.
func TestVPNРежимПоднимаетИСнимаетАдаптер(t *testing.T) {
	layer := &fakeNetLayer{}
	a := vpnApp(t, layer)

	if err := a.Start(); err != nil {
		t.Fatalf("подключение: %v", err)
	}
	layer.mu.Lock()
	prepared, attached := layer.prepared, len(layer.attached)
	layer.mu.Unlock()
	if prepared == 0 || attached != 1 {
		t.Fatalf("Prepare=%d Attach=%d, ожидалось хотя бы 1 и ровно 1", prepared, attached)
	}
	a.mu.Lock()
	sysOn, cfg := a.sysOn, a.tun.Config()
	a.mu.Unlock()
	if sysOn {
		t.Error("в режиме VPN системный прокси включаться не должен")
	}
	if cfg.SocksAddr != "" || cfg.HTTPAddr != "" {
		t.Errorf("локальные прокси должны быть отключены: %q %q", cfg.SocksAddr, cfg.HTTPAddr)
	}

	a.Stop()
	layer.mu.Lock()
	detached := layer.detached
	layer.mu.Unlock()
	if detached == 0 {
		t.Error("после «Отключить» адаптер должен сниматься")
	}
	a.mu.Lock()
	draining := a.draining
	a.mu.Unlock()
	if draining != nil {
		t.Error("в режиме VPN слива быть не должно")
	}
}

// Не поднялся адаптер — подключение считается неудачным, туннель не висит.
func TestVPNОшибкаАдаптераОстанавливаетТуннель(t *testing.T) {
	layer := &fakeNetLayer{attachErr: errors.New("нет драйвера")}
	a := vpnApp(t, layer)

	if err := a.Start(); err == nil {
		t.Fatal("ожидалась ошибка подключения")
	}
	if a.Running() {
		t.Error("туннель не должен считаться запущенным")
	}
}
