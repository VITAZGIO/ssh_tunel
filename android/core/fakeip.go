package core

// Подставные адреса (fake-IP).
//
// Задача такая. Приложение сначала спрашивает у DNS адрес сайта, и только потом
// открывает соединение. Если ответить настоящим адресом, дальше в стек придёт
// адрес — а имя мы уже не узнаем и передадим серверу голый IP. Плохо это двумя
// вещами: сам DNS-запрос ушёл бы наружу открытым текстом, и правила по именам
// («всегда напрямую» для .corp.local и подобное) перестали бы работать.
//
// Поэтому на запрос отдаётся выдуманный адрес из диапазона, зарезервированного
// под тесты сетей, а пара имя-адрес запоминается. Когда приложение придёт с
// соединением на этот адрес, мы вернём имя обратно и отдадим наверх именно его.
// Имя разрешит сервер на том конце туннеля — ровно как на компьютере.

import (
	"fmt"
	"net"
	"sync"
)

// FakePool раздаёт подставные адреса из своей подсети и помнит, кому какой
// достался. Размер подсети ограничивает число одновременно живущих имён:
// когда адреса заканчиваются, самый старый переиспользуется.
type FakePool struct {
	mu sync.Mutex

	base   uint32 // первый выдаваемый адрес
	count  uint32 // сколько всего адресов в подсети пригодно к выдаче
	next   uint32 // смещение следующего
	byName map[string]uint32
	byAddr map[uint32]string
	order  []string // имена в порядке выдачи, для переиспользования
}

// NewFakePool готовит пул по подсети вида 198.18.0.0/15.
//
// Первый адрес подсети не выдаётся: он остаётся за самим интерфейсом.
func NewFakePool(cidr string) (*FakePool, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("подсеть для подставных адресов: %w", err)
	}
	ip4 := ipnet.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("нужна подсеть IPv4, получено %q", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	total := uint32(1) << uint(bits-ones)
	if total < 4 {
		return nil, fmt.Errorf("подсеть %q слишком мала", cidr)
	}
	return &FakePool{
		// +2: нулевой адрес сети и адрес самого интерфейса пропускаем.
		base:   toUint32(ip4) + 2,
		count:  total - 3, // ещё минус широковещательный
		byName: make(map[string]uint32),
		byAddr: make(map[uint32]string),
	}, nil
}

// Get выдаёт подставной адрес для имени. Повторный вызов с тем же именем даёт
// тот же адрес: приложение могло запомнить его и переспросить.
func (p *FakePool) Get(name string) net.IP {
	p.mu.Lock()
	defer p.mu.Unlock()

	if v, ok := p.byName[name]; ok {
		return fromUint32(v)
	}

	addr := p.base + p.next%p.count
	// Круг замкнулся — самое старое имя теряет адрес. Это не потеря данных:
	// приложение спросит заново и получит новый.
	if old, ok := p.byAddr[addr]; ok {
		delete(p.byName, old)
	}
	p.next++

	p.byName[name] = addr
	p.byAddr[addr] = name
	p.order = append(p.order, name)
	return fromUint32(addr)
}

// Name возвращает имя, которому был выдан этот адрес.
func (p *FakePool) Name(ip string) (string, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", false
	}
	ip4 := parsed.To4()
	if ip4 == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	name, ok := p.byAddr[toUint32(ip4)]
	return name, ok
}

// Resolver отдаёт функцию для Handler: по адресу вернуть имя.
func (p *FakePool) Resolver() Resolver { return p.Name }

func toUint32(ip net.IP) uint32 {
	b := ip.To4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func fromUint32(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
