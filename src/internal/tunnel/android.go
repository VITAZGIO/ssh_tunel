package tunnel

// Точка входа для сетевого стека: Android и режим VPN на Windows.
//
// Здесь соединение собрано из IP-пакетов, а не пришло от SOCKS или
// HTTP-прокси. На Android владельца по порту не определить — да и не нужно:
// какие приложения заворачивать, решает сама система
// (VpnService.addAllowedApplication). На Windows владелец виден по порту так же,
// как у прокси, и правила по программам включаются через
// Config.TunProcessRules. Правила «локальная сеть напрямую» и «всегда
// напрямую» действуют всегда: они про адрес назначения, а не про программу.

import (
	"net"
	"sync"
	"time"
)

// ServeConn обслуживает готовое соединение от приложения на телефоне:
// открывает встречное через сервер, пишет событие в лог, считает байты
// и перекачивает данные в обе стороны. Соединение закрывается само.
//
// target — куда шло приложение (имя хоста, если его удалось восстановить,
// иначе адрес), byIP — пришёл готовый адрес, то есть имя разрешалось не нами.
func (t *Tunnel) ServeConn(conn net.Conn, target string, byIP bool) {
	defer conn.Close()

	var proc string
	var pid int
	if t.cfg.TunProcessRules {
		// Адрес приложения у соединения из стека — его собственный сокет,
		// поэтому владелец находится по порту так же, как у прокси.
		proc, pid = lookupProcess(conn)
	}

	// Адрес, выданный телефоном для имени из «всегда напрямую», в списке не
	// значится — там имя. Узнаём его здесь (см. LearnDirect), а в журнал
	// пишем имя: адрес человеку ничего не скажет, и это не утечка DNS — имя
	// разрешали мы сами, по правилу.
	//
	// Имя сверяем с правилами каждый раз: убрали его из списка — адрес,
	// выданный под него раньше, снова идёт через сервер, а не ждёт, пока
	// запись в таблице устареет.
	shown := target
	learned := false
	if name, ok := t.learnedName(target); ok {
		named := name
		if _, port, err := net.SplitHostPort(target); err == nil {
			named = net.JoinHostPort(name, port)
		}
		if t.listedDirect(named) || t.localDirect(named) {
			learned, shown, byIP = true, named, false
		}
	}
	// Программа, которой по правилам положено мимо сервера, идёт напрямую
	// так же, как выученный адрес.
	forceDirect := learned || (proc != "" && !t.useTunnel(proc))

	remote, direct, err := t.dialForTun(target, forceDirect)
	if err != nil {
		t.bus.Publish(eventConn(proc, pid, shown, "tun", byIP, direct, err))
		return
	}
	defer remote.Close()

	t.bus.Publish(eventConn(proc, pid, shown, "tun", byIP, direct, nil))

	t.stats.active.Add(1)
	t.stats.total.Add(1)
	defer t.stats.active.Add(-1)

	t.pump(conn, conn, remote)
}

// dialForTun — решение «через сервер или напрямую» для трафика из стека.
// forceDirect — напрямую в любом случае: адрес выучен под имя из «всегда
// напрямую» или программе по правилам положено мимо сервера.
func (t *Tunnel) dialForTun(target string, forceDirect bool) (net.Conn, bool, error) {
	if m := t.meshTarget(target); m != nil {
		c, err := m.DialPeer(target)
		return c, false, err
	}
	// Слив (см. Drain): связи с сервером уже нет, но интерфейс VPN ещё
	// поднят ради уже открытых сокетов приложений — ведём их напрямую.
	if !t.draining.Load() && !forceDirect && !t.localDirect(target) && !t.listedDirect(target) {
		c, err := t.Dial("tcp", target)
		return c, false, err
	}
	d := t.directDialer(15 * time.Second)
	c, err := d.Dial("tcp", target)
	return c, true, err
}

// learnedTTL — сколько помнить адрес, выданный для имени из «всегда
// напрямую». Сам DNS-ответ живёт секунды, но приложения держат адреса в своём
// кеше куда дольше, а соединения к ним открывают и через десять минут.
// Каждое новое разрешение того же имени продлевает срок.
const learnedTTL = 30 * time.Minute

// learnedMax — потолок таблицы: на случай приложения, которое перебирает
// тысячи имён из списка. Переполнение чистит просроченное, а если и этого
// мало — таблицу целиком: худшее последствие — пара соединений через сервер.
const learnedMax = 4096

type learnedEntry struct {
	name  string
	until time.Time
}

// learnedDirect — адреса, которые телефон сам разрешил для имён из списка
// «всегда напрямую».
//
// Зачем. На телефоне правило по имени срабатывает в DNS: такому имени
// отвечаем настоящим адресом, а не подставным (см. android/core/dns.go). Но
// дальше приложение соединяется уже с адресом, и до ядра доходит
// "142.250.1.1:443", а не имя — список, где записано имя, его не узнаёт, и
// соединение уходило через сервер. Правило по имени на телефоне из-за этого
// не работало вовсе, только по адресам и сетям.
//
// Адреса крупных сервисов общие для многих имён. Но остальные имена
// получают подставные адреса и на настоящий не попадут; сюда может попасть
// лишь приложение, которое зашило адрес в себя, — оно пойдёт напрямую.
type learnedDirect struct {
	mu sync.Mutex
	m  map[string]learnedEntry
}

// LearnDirect запоминает адреса, выданные телефоном для имени из списка
// «всегда напрямую»: соединения на них пойдут мимо сервера.
func (t *Tunnel) LearnDirect(name string, ips []net.IP) {
	l := &t.learned
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = make(map[string]learnedEntry)
	}
	if len(l.m) >= learnedMax {
		for k, e := range l.m {
			if now.After(e.until) {
				delete(l.m, k)
			}
		}
		if len(l.m) >= learnedMax {
			clear(l.m)
		}
	}
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		l.m[ip.String()] = learnedEntry{name: name, until: now.Add(learnedTTL)}
	}
}

// learnedName — имя, под которое телефон выдал этот адрес, если выдавал.
func (t *Tunnel) learnedName(target string) (string, bool) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", false
	}
	l := &t.learned
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip.String()]
	if !ok || time.Now().After(e.until) {
		return "", false
	}
	return e.name, true
}
