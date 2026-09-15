package app

import (
	"net"
	"strconv"
	"testing"
	"time"

	"sshtunnel/internal/config"
)

// freeLocalPort — свободный порт для локальных слушателей туннеля. В
// failover-тестах порты нулевые (слушатели там не нужны), а сливу как раз
// важно, что слушатель занял конкретный порт и не отпустил его.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func drainApp(t *testing.T) (*App, config.Config, string) {
	t.Helper()
	keyPath, pub := testKeyPath(t)
	addr := newFakeSSHServer(t, pub, true)

	p := profileAt(t, "main", addr, keyPath)
	p.SocksPort = freeLocalPort(t)
	p.HTTPPort = freeLocalPort(t)
	cfg := config.Config{Profiles: []config.Profile{p}, ActiveProfile: p.ID}

	a := New(cfg)
	t.Cleanup(a.StopNow)
	if err := a.connectFrom(cfg, cfg.Profiles, 0, 0); err != nil {
		t.Fatalf("подключение: %v", err)
	}
	a.mu.Lock()
	a.running = true
	a.mu.Unlock()
	return a, cfg, net.JoinHostPort("127.0.0.1", strconv.Itoa(p.SocksPort))
}

// «Отключить» из интерфейса больше не закрывает локальный порт мгновенно: в
// нём живут сокеты браузера, и именно их обрыв выглядит как «выключил VPN —
// интернет пропал».
func TestStopОставляетПортЖивымНаВремяСлива(t *testing.T) {
	a, _, socksAddr := drainApp(t)

	a.Stop()

	if a.Running() {
		t.Error("для человека туннель должен быть выключен сразу")
	}
	c, err := net.DialTimeout("tcp", socksAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("порт закрылся сразу после «Отключить»: %v", err)
	}
	c.Close()
}

// Включили обратно — поднимается тот же самый туннель, на том же порту.
// Перезапускать браузер не нужно: его сокеты никуда не девались.
func TestStartПослеStopВозвращаетТотЖеТуннель(t *testing.T) {
	a, _, socksAddr := drainApp(t)

	a.mu.Lock()
	before := a.tun
	a.mu.Unlock()

	a.Stop()
	if err := a.Start(); err != nil {
		t.Fatalf("включение обратно: %v", err)
	}

	a.mu.Lock()
	after, leftover := a.tun, a.draining
	a.mu.Unlock()

	if after != before {
		t.Error("построен новый туннель вместо возврата прежнего — сокеты приложений при этом рвутся")
	}
	if leftover != nil {
		t.Error("ссылка на сливающийся туннель осталась висеть")
	}
	if !a.Running() {
		t.Error("туннель не считается работающим")
	}
	c, err := net.DialTimeout("tcp", socksAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("порт не работает после включения обратно: %v", err)
	}
	c.Close()
}

// StopNow — резкая остановка: порт освобождается сразу. Так гасят по аварии и
// при выходе из программы.
func TestStopNowЗакрываетПортСразу(t *testing.T) {
	a, _, socksAddr := drainApp(t)

	a.StopNow()

	// Слушатель закрывается в Stop синхронно, но дадим ядру мгновение.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", socksAddr, 200*time.Millisecond); err != nil {
			return // порт свободен — этого и ждали
		} else {
			c.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("порт всё ещё занят после резкой остановки")
}

// Смена сервера при работающем туннеле: слушатели те же, меняется только то,
// куда ведёт пул. Для браузера переключение проходит незаметно.
func TestСменаСервераПереиспользуетСлушатели(t *testing.T) {
	keyPath, pub := testKeyPath(t)
	first := newFakeSSHServer(t, pub, true)
	second := newFakeSSHServer(t, pub, true)

	socks, http := freeLocalPort(t), freeLocalPort(t)
	p1 := profileAt(t, "first", first, keyPath)
	p1.SocksPort, p1.HTTPPort = socks, http
	p2 := profileAt(t, "second", second, keyPath)
	p2.SocksPort, p2.HTTPPort = socks, http // порты у профилей обычно одинаковые

	cfg := config.Config{Profiles: []config.Profile{p1, p2}, ActiveProfile: p1.ID}
	a := New(cfg)
	t.Cleanup(a.StopNow)
	if err := a.connectFrom(cfg, []config.Profile{p1}, 0, 0); err != nil {
		t.Fatalf("подключение: %v", err)
	}
	a.mu.Lock()
	a.running = true
	before := a.tun
	a.mu.Unlock()

	if _, err := a.SwitchProfile(p2.ID); err != nil {
		t.Fatalf("смена сервера: %v", err)
	}

	a.mu.Lock()
	after := a.tun
	a.mu.Unlock()
	if after != before {
		t.Error("туннель пересоздан — значит сокеты приложений при смене сервера рвутся")
	}
	if got := a.EffectiveProfileID(); got != p2.ID {
		t.Errorf("активен сервер %q, ожидался %q", got, p2.ID)
	}
}

// Настройки, изменённые во время слива, должны применяться при включении
// обратно. Иначе возврат поднял бы пул по тому, что туннель помнил с прошлого
// раза, — то есть молча подключился бы не туда, куда попросили.
func TestВключениеПослеСливаБерётСвежиеНастройки(t *testing.T) {
	keyPath, pub := testKeyPath(t)
	first := newFakeSSHServer(t, pub, true)
	second := newFakeSSHServer(t, pub, true)

	socks, httpPort := freeLocalPort(t), freeLocalPort(t)
	p := profileAt(t, "main", first, keyPath)
	p.SocksPort, p.HTTPPort = socks, httpPort
	cfg := config.Config{Profiles: []config.Profile{p}, ActiveProfile: p.ID}

	a := New(cfg)
	t.Cleanup(a.StopNow)
	if err := a.connectFrom(cfg, cfg.Profiles, 0, 0); err != nil {
		t.Fatalf("подключение: %v", err)
	}
	a.mu.Lock()
	a.running = true
	a.mu.Unlock()

	a.Stop()

	// Пока шёл слив, человек поправил адрес сервера.
	moved := profileAt(t, "main", second, keyPath)
	moved.SocksPort, moved.HTTPPort = socks, httpPort
	a.mu.Lock()
	a.cfg.Profiles = []config.Profile{moved}
	a.mu.Unlock()

	if err := a.Start(); err != nil {
		t.Fatalf("включение обратно: %v", err)
	}

	a.mu.Lock()
	gotHost := a.tun.Config().Host
	gotPort := a.tun.Config().SSHPort
	a.mu.Unlock()
	if want := net.JoinHostPort(gotHost, strconv.Itoa(gotPort)); want != second {
		t.Errorf("подключились к %s, а настройки указывали на %s", want, second)
	}
}
