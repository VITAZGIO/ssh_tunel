package tunnel

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// ---------- логика цепочки: подставные результаты, без сети ----------

func TestAppendStepОстанавливаетЦепочкуНаПервойНеудаче(t *testing.T) {
	var steps []CheckStep
	stop := appendStep(&steps, CheckStep{Name: StepDNS, OK: true, Code: "resolved"})
	if stop {
		t.Fatal("успешный шаг не должен останавливать цепочку")
	}
	stop = appendStep(&steps, CheckStep{Name: StepPort, OK: false, Code: "timeout"})
	if !stop {
		t.Fatal("провалившийся шаг обязан останавливать цепочку")
	}

	if len(steps) != len(stepOrder) {
		t.Fatalf("шагов получилось %d, ожидалось %d (все, включая пропущенные)", len(steps), len(stepOrder))
	}
	for i, name := range stepOrder {
		if steps[i].Name != name {
			t.Fatalf("шаг %d: получилось %q, ожидалось %q", i, steps[i].Name, name)
		}
	}
	if steps[0].OK != true || steps[0].Skipped {
		t.Error("первый шаг испорчен после остановки цепочки")
	}
	if steps[1].OK || steps[1].Skipped {
		t.Error("провалившийся шаг не должен быть помечен ни как успешный, ни как пропущенный")
	}
	for i := 2; i < len(steps); i++ {
		if !steps[i].Skipped {
			t.Errorf("шаг %q должен быть пропущен после неудачи на %q", steps[i].Name, StepPort)
		}
		if steps[i].OK {
			t.Errorf("пропущенный шаг %q не может быть успешным", steps[i].Name)
		}
	}
}

func TestAppendStepВсеШагиУспешны(t *testing.T) {
	var steps []CheckStep
	for _, name := range stepOrder {
		if appendStep(&steps, CheckStep{Name: name, OK: true, Code: "ok"}) {
			t.Fatalf("успешный шаг %q не должен останавливать цепочку", name)
		}
	}
	if len(steps) != len(stepOrder) {
		t.Fatalf("шагов %d, ожидалось %d", len(steps), len(stepOrder))
	}
	for _, s := range steps {
		if !s.OK || s.Skipped {
			t.Errorf("шаг %q должен быть просто успешным: %+v", s.Name, s)
		}
	}
}

// Неудача на последнем по счёту шаге не должна пытаться дописать несуществующие
// "шаги после последнего" — просто ничего пропущенного не добавляется.
func TestAppendStepНеудачаНаПоследнемШаге(t *testing.T) {
	var steps []CheckStep
	for _, name := range stepOrder[:len(stepOrder)-1] {
		appendStep(&steps, CheckStep{Name: name, OK: true})
	}
	stop := appendStep(&steps, CheckStep{Name: stepOrder[len(stepOrder)-1], OK: false, Code: "mismatch"})
	if !stop {
		t.Fatal("неудача обязана вернуть true")
	}
	if len(steps) != len(stepOrder) {
		t.Fatalf("лишние шаги: %d, ожидалось %d", len(steps), len(stepOrder))
	}
}

// ---------- цепочка целиком, на настоящем (тестовом) SSH-сервере ----------

// Первые четыре шага проверяются на реальном (в процессе) SSH-сервере и
// локальном echo-сервере вместо интернета — быстро и без сети. Дальше идут
// DNS через туннель и открытие сайтов, для которых нужен настоящий интернет:
// таймаут выставлен коротким, чтобы тест не зависел от того, есть ли он у
// песочницы, где он запускается, — важно лишь то, что цепочка останавливается
// и помечает всё после первой неудачи как пропущенное, а не то, чем именно
// закончится сетевая часть.
func TestRunSelfCheckПерваяЧастьЦепочкиПроходитНаТестовомСервере(t *testing.T) {
	dir := t.TempDir()
	keyPath, pub := writeTestKey(t, dir)
	srv := newTestSSHServer(t, pub)
	target := echoServer(t)

	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, _ := strconv.Atoi(portStr)

	opt := SelfCheckOptions{
		Config: Config{
			Host:           host,
			SSHPort:        sshPort,
			User:           "test",
			KeyPath:        keyPath,
			KnownHostsPath: dir + "/known_hosts",
		},
		DialTimeout: 800 * time.Millisecond,
		ProbeAddr:   target.String(),
		// Заведомо недоступные цели — быстро проваливают шаг 5, чтобы не
		// зависеть от интернета в тестовом окружении.
		DNSAddr:      "198.51.100.1:53",
		DNSProbeName: "example.com.",
	}

	steps := RunSelfCheck(context.Background(), opt)
	byName := make(map[string]CheckStep, len(steps))
	for _, s := range steps {
		byName[s.Name] = s
	}

	for _, name := range []string{StepDNS, StepPort, StepKey, StepForward} {
		s := byName[name]
		if !s.OK {
			t.Errorf("шаг %q провалился: %+v", name, s)
		}
	}
	if s := byName[StepDNSTun]; s.OK || s.Skipped {
		t.Errorf("шаг %q должен провалиться (нет DNS-сервера по этому адресу), а не быть %+v", StepDNSTun, s)
	}
	for _, name := range []string{StepSites, StepExternal} {
		if s := byName[name]; !s.Skipped {
			t.Errorf("шаг %q должен быть пропущен после неудачи на %q: %+v", name, StepDNSTun, s)
		}
	}
}

// Неверный ключ (сервер его не примет) — цепочка обязана остановиться на
// шаге 3, не пытаясь пробрасывать что-либо дальше.
func TestRunSelfCheckНеверныйКлючОстанавливаетНаШагеKey(t *testing.T) {
	dir := t.TempDir()
	_, otherPub := writeTestKey(t, t.TempDir()) // ключ, который сервер ожидает
	keyPath, _ := writeTestKey(t, dir)          // а этот у клиента — другой
	srv := newTestSSHServer(t, otherPub)

	host, portStr, _ := net.SplitHostPort(srv.addr)
	sshPort, _ := strconv.Atoi(portStr)

	opt := SelfCheckOptions{
		Config: Config{
			Host:           host,
			SSHPort:        sshPort,
			User:           "test",
			KeyPath:        keyPath,
			KnownHostsPath: dir + "/known_hosts",
		},
		DialTimeout: 2 * time.Second,
	}

	steps := RunSelfCheck(context.Background(), opt)
	byName := make(map[string]CheckStep, len(steps))
	for _, s := range steps {
		byName[s.Name] = s
	}

	if s := byName[StepPort]; !s.OK {
		t.Fatalf("порт обязан ответить: %+v", s)
	}
	keyStep := byName[StepKey]
	if keyStep.OK {
		t.Fatal("чужой ключ не должен быть принят")
	}
	if keyStep.Code != "auth_failed" {
		t.Errorf("code = %q, ожидался auth_failed", keyStep.Code)
	}
	for _, name := range []string{StepForward, StepDNSTun, StepSites, StepExternal} {
		if s := byName[name]; !s.Skipped {
			t.Errorf("шаг %q должен быть пропущен после неудачи на %q", name, StepKey)
		}
	}
}
