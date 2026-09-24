package vpnlayer

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/xjasonlyu/tun2socks/v2/core/device/fdbased"
	"golang.org/x/sys/unix"

	"sshtunnel/android/core"
	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
)

// linuxIfName — имя интерфейса. Короткое: ядро ограничивает имя 15 символами.
const linuxIfName = "sshtun0"

// sys — всё, что режим VPN делает средствами Linux: устройство TUN, его адреса
// и маршруты (через netlink — интерфейс ядра для настройки сети), DNS системы
// и привязка своих сокетов к настоящей сети (SO_BINDTODEVICE).
type sys struct {
	bus *events.Bus

	mu     sync.RWMutex
	ours   int    // номер нашего интерфейса; 0, пока его нет
	up4    string // интерфейс, через который система ходила бы в интернет без нас
	up6    string
	dns    []netip.Addr
	link   netlink.Link
	dnsVia string // "resolved", "file" или "" — как мы настроили DNS системы
}

func newSys(bus *events.Bus) *sys {
	s := &sys{bus: bus}
	// Прошлый запуск мог упасть, не вернув /etc/resolv.conf, — возвращаем
	// сразу, до всего остального.
	if restoreResolvConf() {
		bus.Warnf("Нашёл /etc/resolv.conf, оставшийся от аварийно закрытого прошлого запуска, — вернул как было")
	}
	return s
}

func (s *sys) name() string { return linuxIfName }

// refresh заново находит настоящую сеть. Возвращает true, если она сменилась.
func (s *sys) refresh() bool {
	s.mu.RLock()
	ours := s.ours
	s.mu.RUnlock()

	up4 := findDefault(netlink.FAMILY_V4, ours)
	up6 := findDefault(netlink.FAMILY_V6, ours)
	dns := physicalDNS(up4)

	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.up4 != up4 || s.up6 != up6
	s.up4, s.up6, s.dns = up4, up6, dns
	return changed
}

func (s *sys) hasUplink() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.up4 != ""
}

func (s *sys) dnsServers() []netip.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dns
}

// findDefault — имя интерфейса с маршрутом по умолчанию и наименьшей
// метрикой, кроме нашего.
func findDefault(family int, ours int) string {
	routes, err := netlink.RouteList(nil, family)
	if err != nil {
		return ""
	}
	var cand []defaultRoute
	for _, r := range routes {
		if r.Dst != nil {
			if ones, _ := r.Dst.Mask.Size(); ones != 0 {
				continue
			}
		}
		if r.LinkIndex <= 0 || r.LinkIndex == ours {
			continue
		}
		cand = append(cand, defaultRoute{luid: uint64(r.LinkIndex), ifIndex: uint32(r.LinkIndex), metric: uint32(r.Priority)})
	}
	best, ok := pickDefault(cand, uint64(ours))
	if !ok {
		return ""
	}
	link, err := netlink.LinkByIndex(int(best.ifIndex))
	if err != nil {
		return ""
	}
	return link.Attrs().Name
}

// protect привязывает сокет к настоящей сети: ядро отправит его пакеты через
// эту карту, не глядя на наши маршруты в TUN.
func (s *sys) protect(network, address string, c syscall.RawConn) error {
	s.mu.RLock()
	up4, up6 := s.up4, s.up6
	s.mu.RUnlock()
	if up4 == "" && up6 == "" {
		s.refresh()
		s.mu.RLock()
		up4, up6 = s.up4, s.up6
		s.mu.RUnlock()
	}
	dev := up4
	if strings.HasSuffix(network, "6") && up6 != "" {
		dev = up6
	}
	if dev == "" {
		return nil
	}
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, dev)
	})
	if err != nil {
		return err
	}
	return serr
}

// open создаёт устройство TUN.
func (s *sys) open() (core.Device, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("режиму VPN нужны права root: запусти через sudo")
	}
	tun, err := core.OpenTun(linuxIfName, net.IP(adapterPrefix4.Addr().AsSlice()),
		net.CIDRMask(adapterPrefix4.Bits(), 32), adapterMTU)
	if err != nil {
		return nil, fmt.Errorf("создать устройство TUN: %w", err)
	}
	dev, err := fdbased.Open(strconv.Itoa(tun.FD), adapterMTU, 0)
	if err != nil {
		unix.Close(tun.FD)
		return nil, fmt.Errorf("подключить TUN к стеку: %w", err)
	}
	link, err := netlink.LinkByName(tun.Name)
	if err != nil {
		dev.Close()
		return nil, err
	}
	s.mu.Lock()
	s.ours, s.link = link.Attrs().Index, link
	s.mu.Unlock()
	return dev, nil
}

// configure прописывает маршруты в TUN и направляет туда DNS системы.
func (s *sys) configure() error {
	if err := s.configureRoutes(); err != nil {
		return err
	}
	return s.configureDNS()
}

func (s *sys) configureRoutes() error {
	link := s.link
	// IPv6 может быть выключен в системе целиком — тогда без него, но IPv4
	// обязателен.
	v6 := true
	if err := netlink.AddrAdd(link, &netlink.Addr{IPNet: prefixNet(adapterPrefix6)}); err != nil {
		v6 = false
		s.bus.Warnf("IPv6 на адаптере не настроился (%v) — только IPv4", err)
	}
	for _, r := range routes4 {
		if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: prefixNet(r)}); err != nil {
			return fmt.Errorf("маршрут %s: %w", r, err)
		}
	}
	if v6 {
		for _, r := range routes6 {
			if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: prefixNet(r)}); err != nil {
				s.bus.Warnf("маршрут %s не встал: %v", r, err)
			}
		}
	}

	return nil
}

func (s *sys) configureDNS() error {
	switch {
	case hasResolved():
		// systemd-resolved: DNS на нашем интерфейсе и домен «~.» — все
		// запросы идут только сюда. Исчезнет интерфейс — resolved забудет
		// настройку сам, так что падение программы DNS не ломает.
		cmds := [][]string{
			{"dns", linuxIfName, dnsAddr.String()},
			{"domain", linuxIfName, "~."},
		}
		for _, c := range cmds {
			if out, err := exec.Command("resolvectl", c...).CombinedOutput(); err != nil {
				return fmt.Errorf("resolvectl %s: %v %s", strings.Join(c, " "), err, bytes.TrimSpace(out))
			}
		}
		// Есть не во всех версиях — без неё «~.» и так делает своё дело.
		_ = exec.Command("resolvectl", "default-route", linuxIfName, "yes").Run()
		s.dnsVia = "resolved"
	default:
		if err := replaceResolvConf(); err != nil {
			return err
		}
		s.dnsVia = "file"
	}
	return nil
}

// close возвращает то, что не исчезает вместе с интерфейсом.
func (s *sys) close() {
	switch s.dnsVia {
	case "resolved":
		_ = exec.Command("resolvectl", "revert", linuxIfName).Run()
	case "file":
		if restoreResolvConf() {
			s.bus.Infof("/etc/resolv.conf возвращён как было")
		}
	}
	s.dnsVia = ""
	s.mu.Lock()
	s.ours, s.link = 0, nil
	s.mu.Unlock()
}

// watch зовёт onChange, когда меняется маршрут по умолчанию настоящей сети.
func (s *sys) watch(onChange func()) (func(), error) {
	updates := make(chan netlink.RouteUpdate, 64)
	done := make(chan struct{})
	if err := netlink.RouteSubscribe(updates, done); err != nil {
		return nil, err
	}
	go func() {
		var timer *time.Timer
		for u := range updates {
			if u.Dst != nil {
				if ones, _ := u.Dst.Mask.Size(); ones != 0 {
					continue
				}
			}
			// Смена сети — пачка уведомлений подряд: разбираемся, когда
			// всё утихнет.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(700*time.Millisecond, func() {
				if s.refresh() {
					onChange()
				}
			})
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }, nil
}

func (s *sys) flushDNS() {
	if hasResolved() {
		_ = exec.Command("resolvectl", "flush-caches").Run()
	}
}

func prefixNet(p netip.Prefix) *net.IPNet {
	bits := 32
	if p.Addr().Is6() {
		bits = 128
	}
	return &net.IPNet{IP: net.IP(p.Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), bits)}
}

// hasResolved — работает ли systemd-resolved, и отдаёт ли ему DNS система.
func hasResolved() bool {
	if _, err := exec.LookPath("resolvectl"); err != nil {
		return false
	}
	if _, err := os.Stat("/run/systemd/resolve/resolv.conf"); err != nil {
		return false
	}
	return exec.Command("resolvectl", "status").Run() == nil
}

// physicalDNS — DNS-серверы настоящей сети. У resolved они у каждого
// интерфейса свои; без него — в /etc/resolv.conf (или в его копии, если его
// сейчас подменили мы).
func physicalDNS(uplink string) []netip.Addr {
	var out []netip.Addr
	if uplink != "" && hasResolved() {
		if b, err := exec.Command("resolvectl", "dns", uplink).Output(); err == nil {
			// «Link 2 (eth0): 192.168.1.1 fe80::1%eth0»
			line := string(b)
			if i := strings.Index(line, "):"); i >= 0 {
				line = line[i+2:]
			}
			out = append(out, parseAddrs(strings.Fields(line))...)
		}
		if len(out) == 0 {
			out = append(out, nameservers("/run/systemd/resolve/resolv.conf")...)
		}
		return out
	}
	if _, err := os.Stat(resolvBackup()); err == nil {
		return nameservers(resolvBackup())
	}
	return nameservers("/etc/resolv.conf")
}

func nameservers(path string) []netip.Addr {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var fields []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "nameserver" {
			fields = append(fields, f[1])
		}
	}
	return parseAddrs(fields)
}

// parseAddrs оставляет только годные адреса, кроме локальных заглушек
// (127.0.0.53 у resolved) и нашего собственного DNS.
func parseAddrs(fields []string) []netip.Addr {
	var out []netip.Addr
	for _, f := range fields {
		a, err := netip.ParseAddr(f)
		if err != nil {
			continue
		}
		a = a.Unmap()
		if a.IsLoopback() || a == dnsAddr || a.Zone() != "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Без resolved DNS системы задаётся файлом /etc/resolv.conf. Его прежнее
// состояние (файл или ссылка) сохраняется в папку настроек и возвращается
// при выключении — или при следующем запуске, если программа упала.

func resolvBackup() string { return filepath.Join(config.Dir(), "resolv.conf.backup") }
func resolvLink() string   { return filepath.Join(config.Dir(), "resolv.conf.link") }

func replaceResolvConf() error {
	const path = "/etc/resolv.conf"
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	if target, err := os.Readlink(path); err == nil {
		if err := os.WriteFile(resolvLink(), []byte(target), 0o600); err != nil {
			return err
		}
	}
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(resolvBackup(), old, 0o600); err != nil {
		return err
	}
	os.Remove(path) // ссылку — убрать, а не писать сквозь неё в чужой файл
	content := "# ssh_tunnel VPN: вернётся как было при выключении\nnameserver " + dnsAddr.String() + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

func restoreResolvConf() bool {
	const path = "/etc/resolv.conf"
	old, err := os.ReadFile(resolvBackup())
	if err != nil {
		return false
	}
	os.Remove(path)
	if target, err := os.ReadFile(resolvLink()); err == nil {
		err = os.Symlink(string(target), path)
		if err == nil {
			os.Remove(resolvLink())
		}
	} else {
		err = os.WriteFile(path, old, 0o644)
	}
	if err == nil {
		os.Remove(resolvBackup())
	}
	return err == nil
}
