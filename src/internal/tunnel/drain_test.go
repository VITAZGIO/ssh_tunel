package tunnel

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"sshtunnel/internal/events"
)

// drainTunnel — туннель с коротким таймаутом слива: ждать по-настоящему
// сорок пять секунд в тестах, разумеется, нельзя.
func drainTunnel(t *testing.T, timeout time.Duration) (*Tunnel, string, *testSSHServer) {
	t.Helper()
	tun, socksAddr, _, srv := startTunnel(t, 1)
	tun.mu.Lock()
	tun.cfg.DrainTimeout = timeout
	tun.mu.Unlock()
	return tun, socksAddr, srv
}

// Главное свойство слива: слушатели остаются на месте. Именно в них живут
// сокеты браузера, и закрытие порта — это и есть «выключил туннель, интернет
// пропал, помогает только перезапуск браузера».
func TestDrainОставляетСлушателиНоРветСервер(t *testing.T) {
	tun, socksAddr, _ := drainTunnel(t, 10*time.Second)

	tun.Drain()

	if !tun.Draining() {
		t.Fatal("слив не начался")
	}
	// Для внешнего мира туннель выключен сразу же — как и раньше.
	if got := tun.State(); got != events.StateStopped {
		t.Errorf("состояние %q, ожидалось %q", got, events.StateStopped)
	}
	if got := len(tun.snapLinks()); got != 0 {
		t.Errorf("осталось SSH-соединений: %d, ожидалось 0", got)
	}
	c, err := net.Dial("tcp", socksAddr)
	if err != nil {
		t.Fatalf("локальный порт закрылся вместе с туннелем: %v", err)
	}
	c.Close()
}

// Соединение, открытое во время слива, должно дойти до цели напрямую, мимо
// сервера: сервер к этому моменту уже отпущен.
func TestDrainВедётСоединенияНапрямую(t *testing.T) {
	tun, socksAddr, srv := drainTunnel(t, 10*time.Second)
	target := echoServer(t)

	tun.Drain()
	before := srv.channels.Load()

	conn := socks5Connect(t, socksAddr, "127.0.0.1", target.Port, false)
	defer conn.Close()
	fmt.Fprintf(conn, "GET /hello HTTP/1.0\r\nHost: x\r\n\r\n")
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("чтение ответа: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("через сливающийся прокси ничего не пришло")
	}
	if got := srv.channels.Load(); got != before {
		t.Errorf("во время слива открыто каналов к серверу: %d, ожидалось 0", got-before)
	}
}

// По истечении таймаута слив обязан закончиться настоящей остановкой: порт
// освобождается, иначе следующий запуск программы его не займёт.
func TestDrainЗакрываетСлушателиПоТаймауту(t *testing.T) {
	tun, socksAddr, _ := drainTunnel(t, 400*time.Millisecond)

	tun.Drain()
	waitFor(t, 3*time.Second, func() bool { return !tun.Draining() })

	if _, err := net.Dial("tcp", socksAddr); err == nil {
		t.Error("порт всё ещё занят после конца слива")
	}
	if got := tun.State(); got != events.StateStopped {
		t.Errorf("состояние %q, ожидалось %q", got, events.StateStopped)
	}
}

// Досрочный выход: к нам перестали обращаться — значит доживать нечего.
// Проверяем именно паузу тишины, а не «ноль активных прямо сейчас»: в момент
// слива активные обнуляются сами собой, и без паузы порт закрывался бы
// мгновенно — ровно в ту секунду, когда браузер постучится снова.
func TestDrainЗаканчиваетсяДосрочноВТишине(t *testing.T) {
	tun, _, _ := drainTunnel(t, 4*time.Second)

	start := time.Now()
	tun.Drain()
	waitFor(t, 3*time.Second, func() bool { return !tun.Draining() })

	if took := time.Since(start); took >= 4*time.Second {
		t.Errorf("слив ждал полный таймаут (%v), хотя обращений не было", took)
	}
}

// «Подключить» во время слива поднимает тот же самый туннель: слушатели не
// закрывались, а значит уже открытые сокеты приложений просто снова пойдут
// через сервер — перезапускать браузер не нужно.
func TestResumeВозвращаетТуннельНеТрогаяСлушатели(t *testing.T) {
	tun, socksAddr, srv := drainTunnel(t, 10*time.Second)
	target := echoServer(t)

	tun.Drain()
	if err := tun.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if tun.Draining() {
		t.Error("слив не отменён")
	}
	if got := tun.State(); got != events.StateConnected {
		t.Errorf("состояние %q, ожидалось %q", got, events.StateConnected)
	}

	before := srv.channels.Load()
	conn := socks5Connect(t, socksAddr, "127.0.0.1", target.Port, false)
	defer conn.Close()
	fmt.Fprintf(conn, "GET /hello HTTP/1.0\r\nHost: x\r\n\r\n")
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("чтение ответа: %v", err)
	}
	if got := srv.channels.Load(); got <= before {
		t.Error("после Resume трафик не пошёл через сервер")
	}
}

// Rebind — смена сервера без закрытия слушателей. Локальные порты обязаны
// совпадать: слушатели остались от прежних настроек.
func TestRebindТребуетТехЖеПортов(t *testing.T) {
	tun, _, _ := drainTunnel(t, 10*time.Second)
	tun.Drain()

	other := tun.cfg
	other.SocksAddr = "127.0.0.1:" + strconv.Itoa(freePort(t))
	if err := tun.Rebind(other); err == nil {
		t.Error("смена локального порта должна быть ошибкой — слушатель остался прежний")
	}
}

func TestRebindИResumeОтвергаютНеСливающийсяТуннель(t *testing.T) {
	tun, _, _ := drainTunnel(t, 10*time.Second)

	if err := tun.Resume(); err == nil {
		t.Error("Resume на работающем туннеле должен быть ошибкой")
	}
	if err := tun.Rebind(tun.cfg); err == nil {
		t.Error("Rebind на работающем туннеле должен быть ошибкой")
	}
}

// Stop во время слива добивает его немедленно — это повторное нажатие
// «Отключить» из интерфейса.
func TestStopДобиваетСлив(t *testing.T) {
	tun, socksAddr, _ := drainTunnel(t, 10*time.Second)

	tun.Drain()
	tun.Stop()

	if tun.Draining() {
		t.Error("слив не завершён")
	}
	if _, err := net.Dial("tcp", socksAddr); err == nil {
		t.Error("порт всё ещё занят после Stop")
	}
}

// Живая проверка ровно того, на что жалуется человек: «выключил VPN — в
// браузере интернет пропал». Браузер в этот момент держит открытый сокет до
// нашего прокси и шлёт в него следующий запрос. Проверяем, что этот
// следующий запрос доходит, а не упирается в закрытый порт.
func TestЖивьёмВыключениеНеЛомаетСледующийЗапрос(t *testing.T) {
	tun, socksAddr, _ := drainTunnel(t, 10*time.Second)
	target := echoServer(t)

	// До выключения — запрос через сервер.
	first := socks5Connect(t, socksAddr, "127.0.0.1", target.Port, false)
	fmt.Fprintf(first, "GET /hello HTTP/1.0\r\nHost: x\r\n\r\n")
	if _, err := io.ReadAll(first); err != nil {
		t.Fatalf("до выключения не работает: %v", err)
	}
	first.Close()

	// Человек нажал «Отключить».
	tun.Drain()

	// Браузер шлёт следующий запрос — он обязан дойти.
	for i := 0; i < 3; i++ {
		c, err := net.DialTimeout("tcp", socksAddr, 2*time.Second)
		if err != nil {
			t.Fatalf("запрос №%d после выключения упёрся в закрытый порт: %v", i+1, err)
		}
		c.Close()
		next := socks5Connect(t, socksAddr, "127.0.0.1", target.Port, false)
		fmt.Fprintf(next, "GET /hello HTTP/1.0\r\nHost: x\r\n\r\n")
		body, err := io.ReadAll(next)
		next.Close()
		if err != nil || len(body) == 0 {
			t.Fatalf("запрос №%d после выключения не прошёл: %v", i+1, err)
		}
	}

	// И обратно: включили — тот же порт, те же сокеты, снова через сервер.
	if err := tun.Resume(); err != nil {
		t.Fatalf("включение обратно: %v", err)
	}
	back := socks5Connect(t, socksAddr, "127.0.0.1", target.Port, false)
	defer back.Close()
	fmt.Fprintf(back, "GET /hello HTTP/1.0\r\nHost: x\r\n\r\n")
	if body, err := io.ReadAll(back); err != nil || len(body) == 0 {
		t.Fatalf("после включения обратно не работает: %v", err)
	}
}

func waitFor(t *testing.T, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("не дождались нужного состояния")
}
