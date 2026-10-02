package app

import (
	"net"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/config"
)

// fastLaunchRetry укорачивает ожидание StartOnLaunch на время теста.
func fastLaunchRetry(t *testing.T, retryFor, every time.Duration) {
	t.Helper()
	oldFor, oldEvery := launchRetryFor, launchRetryEvery
	launchRetryFor, launchRetryEvery = retryFor, every
	t.Cleanup(func() { launchRetryFor, launchRetryEvery = oldFor, oldEvery })
}

func runLaunch(a *App) <-chan struct{} {
	done := make(chan struct{})
	go func() { a.StartOnLaunch(); close(done) }()
	return done
}

func waitDone(t *testing.T, done <-chan struct{}, within time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("StartOnLaunch не вернулся за %v", within)
	}
}

// Сервер недоступен в первые секунды (при входе в систему ещё не поднялся
// Wi-Fi) — автозапуск дожидается сети и подключается сам, без «Подключить».
func TestStartOnLaunchWaitsForNetwork(t *testing.T) {
	fastLaunchRetry(t, 10*time.Second, 50*time.Millisecond)
	keyPath, pub := testKeyPath(t)
	addr := closedPort(t)

	a := New(config.Config{Profiles: []config.Profile{profileAt(t, "main", addr, keyPath)}})
	t.Cleanup(a.Stop)
	done := runLaunch(a)

	time.Sleep(300 * time.Millisecond)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("порт %s успели занять: %v", addr, err)
	}
	serveFakeSSH(t, ln, fakeSSHConfig(t, pub, true))

	waitDone(t, done, 5*time.Second)
	if !a.Running() {
		t.Fatal("туннель не поднялся после появления сети")
	}
}

// Отказ по ключу ожиданием не лечится — пробовать снова незачем.
func TestStartOnLaunchStopsOnAuthError(t *testing.T) {
	fastLaunchRetry(t, 10*time.Second, 50*time.Millisecond)
	keyPath, pub := testKeyPath(t)
	addr := newFakeSSHServer(t, pub, false)

	a := New(config.Config{Profiles: []config.Profile{profileAt(t, "main", addr, keyPath)}})
	t.Cleanup(a.Stop)

	logs := collectLogs(a.Bus, a.StartOnLaunch)
	joined := strings.Join(logs, "\n")
	if a.Running() {
		t.Fatal("туннель поднялся с отвергнутым ключом")
	}
	if strings.Contains(joined, "пробую снова") {
		t.Fatalf("после отказа по ключу пробовал снова:\n%s", joined)
	}
	if !strings.Contains(joined, "Автозапуск не удался") {
		t.Fatalf("в журнале нет причины:\n%s", joined)
	}
}

// Сеть так и не появилась — через отведённое время сдаёмся с ошибкой в
// журнале, а не крутимся вечно.
func TestStartOnLaunchGivesUp(t *testing.T) {
	fastLaunchRetry(t, 300*time.Millisecond, 50*time.Millisecond)
	keyPath, _ := testKeyPath(t)
	a := New(config.Config{Profiles: []config.Profile{profileAt(t, "main", closedPort(t), keyPath)}})
	t.Cleanup(a.Stop)

	waitDone(t, runLaunch(a), 5*time.Second)
	if a.Running() {
		t.Fatal("туннель поднялся к закрытому порту")
	}
}

// Человек сам нажал «Отключить», пока автозапуск ждал сеть, — его решение
// важнее: повторные попытки прекращаются.
func TestStartOnLaunchYieldsToUser(t *testing.T) {
	fastLaunchRetry(t, time.Minute, 200*time.Millisecond)
	keyPath, pub := testKeyPath(t)
	addr := closedPort(t)
	a := New(config.Config{Profiles: []config.Profile{profileAt(t, "main", addr, keyPath)}})
	t.Cleanup(a.Stop)

	done := runLaunch(a)
	time.Sleep(50 * time.Millisecond)
	a.Stop()
	waitDone(t, done, 2*time.Second)

	// Сервер появился уже после — автозапуск не должен включить туннель сам.
	if ln, err := net.Listen("tcp", addr); err == nil {
		serveFakeSSH(t, ln, fakeSSHConfig(t, pub, true))
	}
	time.Sleep(300 * time.Millisecond)
	if a.Running() {
		t.Fatal("автозапуск включил туннель после «Отключить»")
	}
}
