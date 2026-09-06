package app

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
)

// ---------- подставной SSH-сервер ----------
//
// Ровно настолько подробный, насколько нужно App.Start()/connectFrom: принять
// TCP, провести SSH-рукопожатие, принять или отвергнуть ключ. Пробрасывать
// каналы не умеет и не должен — до этого дело в тестах не доходит.

func newFakeSSHServer(t *testing.T, clientPub ssh.PublicKey, acceptKey bool) string {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if acceptKey && bytes.Equal(key.Marshal(), clientPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("отказано")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					nc.Reject(ssh.UnknownChannelType, "не поддерживается в тесте")
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// closedPort — адрес, где заведомо никто не слушает: TCP-подключение к нему
// отказывает почти мгновенно, без таймаута.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func testKeyPath(t *testing.T) (path string, pub ssh.PublicKey) {
	t.Helper()
	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatal(err)
	}
	return path, sshPub
}

func profileAt(t *testing.T, id, addr, keyPath string) config.Profile {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	fmt.Sscanf(portStr, "%d", &port)
	return config.Profile{
		ID: id, Name: id, Host: host, SSHPort: port, User: "test", KeyPath: keyPath,
		PoolSize: 1, SocksPort: 0, HTTPPort: 0,
	}
}

func collectLogs(bus *events.Bus, fn func()) []string {
	ch, unsub := bus.Subscribe()
	defer unsub()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	<-done
	var out []string
	for {
		select {
		case e := <-ch:
			if e.Kind == events.KindLog {
				out = append(out, e.Text)
			}
		default:
			return out
		}
	}
}

// ---------- тесты ----------

// Ранжирование по отклику — на подставных результатах замеров, без сети:
// ответившие быстрее идут первыми, неответившие — в конец.
func TestRankByLatencyOrdersFastestFirst(t *testing.T) {
	profiles := []config.Profile{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	results := []ProfileLatency{
		{ID: "a", dur: 300 * time.Millisecond},
		{ID: "b", dur: 50 * time.Millisecond},
		{ID: "c", Error: "не ответил"},
	}
	ranked := rankByLatency(profiles, results)
	want := []string{"b", "a", "c"}
	for i, id := range want {
		if ranked[i].ID != id {
			t.Fatalf("порядок %v, ожидался %v", idsOf(ranked), want)
		}
	}
}

func idsOf(ps []config.Profile) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

// Все серверы недоступны — порядок не должен ломаться (остаётся как в
// исходном списке), а не зависеть от карты с недетерминированным обходом.
func TestRankByLatencyAllFailedKeepsOrder(t *testing.T) {
	profiles := []config.Profile{{ID: "a"}, {ID: "b"}}
	results := []ProfileLatency{
		{ID: "a", Error: "нет ответа"},
		{ID: "b", Error: "нет ответа"},
	}
	ranked := rankByLatency(profiles, results)
	if ranked[0].ID != "a" || ranked[1].ID != "b" {
		t.Fatalf("порядок %v, ожидался [a b]", idsOf(ranked))
	}
}

// Основной сервер недоступен — подключение уходит на запасной, и это видно
// в журнале.
func TestConnectFromFailsOverToNextOnUnreachable(t *testing.T) {
	keyPath, pub := testKeyPath(t)
	primary := closedPort(t) // никто не слушает — «отказано в соединении»
	backup := newFakeSSHServer(t, pub, true)

	cfg := config.Config{Profiles: []config.Profile{
		profileAt(t, "primary", primary, keyPath),
		profileAt(t, "backup", backup, keyPath),
	}}
	a := New(cfg)
	t.Cleanup(a.Stop)

	var err error
	logs := collectLogs(a.Bus, func() {
		err = a.connectFrom(cfg, cfg.Profiles, 0, 0)
	})
	if err != nil {
		t.Fatalf("connectFrom вернул ошибку: %v", err)
	}
	if got := a.EffectiveProfileID(); got != "backup" {
		t.Fatalf("подключены к %q, ожидался backup", got)
	}
	if !containsSubstring(logs, "запасной") {
		t.Errorf("в журнале нет упоминания перехода на запасной сервер: %v", logs)
	}
}

// Ключ не подошёл — на запасной сервер переходить нельзя: это не починит
// другой сервер, а тихое подключение не туда хуже честной ошибки.
func TestConnectFromStopsOnAuthError(t *testing.T) {
	keyPath, pub := testKeyPath(t)
	primary := newFakeSSHServer(t, pub, false) // сервер ждёт другой ключ
	backupAddr := newFakeSSHServer(t, pub, true)

	cfg := config.Config{Profiles: []config.Profile{
		profileAt(t, "primary", primary, keyPath),
		profileAt(t, "backup", backupAddr, keyPath),
	}}
	a := New(cfg)
	t.Cleanup(a.Stop)

	err := a.connectFrom(cfg, cfg.Profiles, 0, 0)
	if err == nil {
		t.Fatal("ожидалась ошибка — ключ не принят")
	}
	if a.EffectiveProfileID() != "" {
		t.Errorf("не должны были подключиться никуда, а подключены к %q", a.EffectiveProfileID())
	}
}

// Переход на запасной сервер при обрыве связи (а не только при неудачном
// первом подключении): наблюдатель видит устойчивое "переподключение" и сам
// переключается.
func TestWatchFailoverSwitchesOnSustainedReconnecting(t *testing.T) {
	old := failoverGrace
	failoverGrace = 50 * time.Millisecond
	t.Cleanup(func() { failoverGrace = old })

	keyPath, pub := testKeyPath(t)
	backup := newFakeSSHServer(t, pub, true)

	profiles := []config.Profile{
		{ID: "primary", Name: "primary", Host: "203.0.113.1", SSHPort: 22, User: "test", KeyPath: keyPath, PoolSize: 1},
		profileAt(t, "backup", backup, keyPath),
	}
	cfg := config.Config{Profiles: profiles}
	a := New(cfg)
	t.Cleanup(a.Stop)

	// Симулируем, что «primary» уже подключён и работает (без настоящего
	// соединения — это состояние проверяет само подключение, а не то, как оно
	// возникло).
	a.mu.Lock()
	a.running = true
	a.effectiveProfile = "primary"
	a.gen = 1
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.watchFailover(cfg, profiles, 0, 1)
		close(done)
	}()

	// watchFailover подписывается на шину внутри своей горутины — сколько
	// именно ждать её планировщику, заранее не угадать, особенно под
	// нагрузкой всего пакета тестов разом (как в CI). Фиксированная пауза
	// перед первым событием этого не даёт: событие, отправленное до
	// подписки, теряется навсегда (обычная семантика pub/sub), и тест
	// зависает или мигает. Вместо угаданной паузы шлём "тестовый обрыв"
	// раз в 10мс, пока горутина не завершится сама или не истечёт запас
	// времени, — reconnectingSince внутри watchFailover выставляется по
	// ПЕРВОМУ полученному событию, какое бы оно ни было по счёту, поэтому
	// лишние повторы ничего не портят: как только с момента первого
	// дошедшего события пройдёт failoverGrace, переход случится сам.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-done:
			break loop
		case <-deadline:
			t.Fatal("watchFailover не завершился — переход на запасной не случился")
		case <-ticker.C:
			a.Bus.State(events.StateReconnecting, "тестовый обрыв")
		}
	}

	if got := a.EffectiveProfileID(); got != "backup" {
		t.Fatalf("после устойчивого обрыва подключены к %q, ожидался backup", got)
	}
}

func containsSubstring(lines []string, sub string) bool {
	for _, l := range lines {
		if len(sub) == 0 {
			return true
		}
		if idx := indexOf(l, sub); idx >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
