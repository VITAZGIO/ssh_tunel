package mesh

// Проверка NAT: смогут ли устройства когда-нибудь соединиться напрямую.
//
// NAT — роутер или оператор, который прячет устройство за общим адресом.
// Когда устройство шлёт UDP-пакет наружу, NAT заводит «дырку»: внутренний
// адрес:порт ↔ внешний адрес:порт. Для прямого соединения важно, как он это
// делает, и это можно измерить через сервер (meshd отвечает по STUN на двух
// UDP-портах, см. cmd/meshd):
//
//   - сопоставление (mapping): один сокет шлёт на порт 1, потом на порт 2
//     сервера. Внешний порт одинаковый — NAT «простой» (не зависит от
//     собеседника, узнанный через сервер адрес сгодится и другому
//     устройству). Разный — «жёсткий»: под каждого собеседника свой порт;
//   - фильтрация (filtering): свежий сокет пишет на порт 1 и просит ответить
//     с порта 2. Дошло — NAT пускает на открытую дырку кого угодно;
//   - порядок портов: несколько сокетов подряд — растут ли внешние порты на
//     постоянный шаг (тогда жёсткий NAT можно угадать);
//   - IPv6: у устройства настоящий IPv6-адрес без NAT.
//
// Пакеты идут МИМО туннеля (на Android и в режиме VPN — помеченным сокетом):
// иначе проверялся бы NAT сервера, а не свой.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// NATInfo — результат проверки; уходит на сервер (op "nat") и в Status.
type NATInfo struct {
	Tested        int64  `json:"tested"`
	UDP           bool   `json:"udp"`
	Mapping       string `json:"mapping,omitempty"`   // independent / dependent
	Filtering     string `json:"filtering,omitempty"` // open / restricted
	PublicIP      string `json:"publicIp,omitempty"`
	PortPreserved bool   `json:"portPreserved,omitempty"`
	PortDelta     int    `json:"portDelta,omitempty"`
	IPv6          bool   `json:"ipv6,omitempty"`
}

// ProbeEnv — где и как проверять.
type ProbeEnv struct {
	// Server — адрес сервера (имя или IP), Ports — его порты проверки.
	Server string
	Ports  []int
	// Listen открывает UDP-сокет мимо туннеля; nil — обычный.
	Listen func(network string) (net.PacketConn, error)
	// Resolver — чем искать адрес сервера; nil — системный.
	Resolver *net.Resolver
	// Timeout — ожидание одного ответа; 0 — 700 мс (три попытки на запрос).
	Timeout time.Duration
}

const (
	stunMagic      = 0x2112A442
	stunBindReq    = 0x0001
	stunBindResp   = 0x0101
	stunAttrMapped = 0x0020
	stunAttrChange = 0x0003
	stunChangePort = 0x02
)

// ProbeNAT проверяет NAT этого устройства. Сервер без проверки NAT (или UDP,
// режущийся по пути) даёт UDP=false.
func ProbeNAT(ctx context.Context, env ProbeEnv) NATInfo {
	info := NATInfo{Tested: time.Now().Unix()}
	if env.Server == "" || len(env.Ports) == 0 {
		return info
	}
	if env.Listen == nil {
		env.Listen = func(network string) (net.PacketConn, error) { return net.ListenPacket(network, ":0") }
	}
	if env.Timeout <= 0 {
		env.Timeout = 700 * time.Millisecond
	}
	info.IPv6 = hasGlobalIPv6()

	ip, err := resolve4(ctx, env)
	if err != nil {
		return info
	}
	dst := func(i int) *net.UDPAddr {
		return &net.UDPAddr{IP: ip, Port: env.Ports[i%len(env.Ports)]}
	}

	// 1–2. Сопоставление: один сокет, два порта сервера.
	pc, err := env.Listen("udp4")
	if err != nil {
		return info
	}
	defer pc.Close()
	m1, err := stunExchange(ctx, pc, dst(0), false, env.Timeout)
	if err != nil {
		return info
	}
	info.UDP = true
	info.PublicIP = m1.Addr().String()
	if la, ok := pc.LocalAddr().(*net.UDPAddr); ok && la.Port == int(m1.Port()) {
		info.PortPreserved = true
	}
	if len(env.Ports) > 1 {
		if m2, err := stunExchange(ctx, pc, dst(1), false, env.Timeout); err == nil {
			if m2 == m1 {
				info.Mapping = "independent"
			} else {
				info.Mapping = "dependent"
			}
		}
	}

	// 3. Фильтрация: свежий сокет, писал только на порт 1 — пустит ли NAT
	// ответ с порта 2.
	if len(env.Ports) > 1 {
		if pf, err := env.Listen("udp4"); err == nil {
			if _, err := stunExchange(ctx, pf, dst(0), false, env.Timeout); err == nil {
				if _, err := stunExchange(ctx, pf, dst(0), true, env.Timeout); err == nil {
					info.Filtering = "open"
				} else {
					info.Filtering = "restricted"
				}
			}
			pf.Close()
		}
	}

	// 4. Шаг портов: три сокета подряд.
	var ports []int
	for i := 0; i < 3; i++ {
		ps, err := env.Listen("udp4")
		if err != nil {
			break
		}
		if m, err := stunExchange(ctx, ps, dst(0), false, env.Timeout); err == nil {
			ports = append(ports, int(m.Port()))
		}
		ps.Close()
	}
	if len(ports) == 3 && ports[1]-ports[0] == ports[2]-ports[1] {
		info.PortDelta = ports[1] - ports[0]
	}
	return info
}

func resolve4(ctx context.Context, env ProbeEnv) (net.IP, error) {
	if ip := net.ParseIP(env.Server); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4, nil
		}
		return nil, errors.New("нужен IPv4-адрес")
	}
	r := env.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := r.LookupIP(rctx, "ip4", env.Server)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("не нашёл адрес сервера")
	}
	return ips[0].To4(), nil
}

// hasGlobalIPv6 — есть ли у устройства настоящий (не локальный) IPv6.
func hasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() != nil {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		// 2000::/3 — глобальные адреса; fc00::/7 (частные) и fe80:: не в счёт.
		if ok && ip.IsGlobalUnicast() && !ip.IsPrivate() && ip.As16()[0]&0xe0 == 0x20 {
			return true
		}
	}
	return false
}

// stunExchange шлёт запрос и ждёт ответ с тем же id (три попытки).
// changePort — просить сервер ответить с другого порта.
func stunExchange(ctx context.Context, pc net.PacketConn, dst *net.UDPAddr, changePort bool, timeout time.Duration) (netip.AddrPort, error) {
	var id [12]byte
	rand.Read(id[:])
	req := stunRequest(id, changePort)
	buf := make([]byte, 1500)
	for try := 0; try < 3; try++ {
		if ctx.Err() != nil {
			return netip.AddrPort{}, ctx.Err()
		}
		if _, err := pc.WriteTo(req, dst); err != nil {
			return netip.AddrPort{}, err
		}
		deadline := time.Now().Add(timeout)
		for {
			pc.SetReadDeadline(deadline)
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				break // тишина — следующая попытка
			}
			if ap, ok := stunParse(buf[:n], id); ok {
				pc.SetReadDeadline(time.Time{})
				return ap, nil
			}
		}
	}
	pc.SetReadDeadline(time.Time{})
	return netip.AddrPort{}, errors.New("нет ответа по UDP")
}

// stunRequest — Binding-запрос с атрибутом CHANGE-REQUEST (сервер без него
// не отвечает: так ответ не бывает заметно больше запроса).
func stunRequest(id [12]byte, changePort bool) []byte {
	b := make([]byte, 28)
	binary.BigEndian.PutUint16(b[0:2], stunBindReq)
	binary.BigEndian.PutUint16(b[2:4], 8)
	binary.BigEndian.PutUint32(b[4:8], stunMagic)
	copy(b[8:20], id[:])
	binary.BigEndian.PutUint16(b[20:22], stunAttrChange)
	binary.BigEndian.PutUint16(b[22:24], 4)
	if changePort {
		b[27] = stunChangePort
	}
	return b
}

// stunParse достаёт XOR-MAPPED-ADDRESS из ответа с нужным id.
func stunParse(b []byte, id [12]byte) (netip.AddrPort, bool) {
	if len(b) < 20 || binary.BigEndian.Uint16(b[0:2]) != stunBindResp ||
		binary.BigEndian.Uint32(b[4:8]) != stunMagic || string(b[8:20]) != string(id[:]) {
		return netip.AddrPort{}, false
	}
	key := b[4:20]
	for a := b[20:]; len(a) >= 4; {
		typ, l := binary.BigEndian.Uint16(a[0:2]), int(binary.BigEndian.Uint16(a[2:4]))
		padded := (l + 3) &^ 3
		if 4+padded > len(a) {
			break
		}
		v := a[4 : 4+l]
		if typ == stunAttrMapped && l >= 8 {
			port := binary.BigEndian.Uint16(v[2:4]) ^ uint16(stunMagic>>16)
			raw := make([]byte, l-4)
			for i := range raw {
				raw[i] = v[4+i] ^ key[i]
			}
			if ip, ok := netip.AddrFromSlice(raw); ok {
				return netip.AddrPortFrom(ip.Unmap(), port), true
			}
		}
		a = a[4+padded:]
	}
	return netip.AddrPort{}, false
}

// natSummary — одной строкой для журнала.
func natSummary(n NATInfo) string {
	if !n.UDP {
		return "UDP до сервера не проходит — прямые соединения невозможны"
	}
	s := "NAT "
	switch n.Mapping {
	case "independent":
		s += "простой"
	case "dependent":
		s += "жёсткий"
	default:
		s += "не определён"
	}
	if n.Filtering == "open" {
		s += ", открытый"
	}
	if n.PortDelta != 0 {
		s += ", порты по порядку (шаг " + strconv.Itoa(n.PortDelta) + ")"
	}
	if n.IPv6 {
		s += ", есть IPv6"
	}
	return s + ", внешний адрес " + n.PublicIP
}
