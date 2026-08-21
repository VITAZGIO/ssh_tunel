//go:build linux

package core

// Настоящее tun-устройство для проверок.
//
// На Android дескриптор выдаёт VpnService.Builder.establish() — уже настроенный,
// с адресами и маршрутами. Здесь то же самое приходится делать руками, чтобы
// прогнать через стек не выдуманные, а настоящие пакеты, которые сформировало
// ядро Linux. Утилиты ip в контейнере нет, поэтому всё через ioctl.

import (
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ifreq — та самая структура из <linux/if.h>: имя интерфейса и объединение,
// смысл которого зависит от команды. Держим её байтами, чтобы не гадать с
// выравниванием.
type ifreq struct {
	name [16]byte
	data [24]byte
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// Tun — открытое устройство: дескриптор и имя, которое дало ядро.
type Tun struct {
	FD   int
	Name string
}

// OpenTun создаёт интерфейс, вешает на него адрес с маской и поднимает его.
// Отдельный маршрут прописывать не надо: ядро само добавит маршрут на подсеть,
// а этого хватает, чтобы трафик на выдуманные адреса пошёл в наш стек.
func OpenTun(name string, addr net.IP, mask net.IPMask, mtu int) (*Tun, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("открыть /dev/net/tun: %w", err)
	}

	var req ifreq
	copy(req.name[:], name)
	// IFF_TUN — пакеты уровня IP, IFF_NO_PI — без служебного заголовка,
	// ровно в таком виде их отдаёт и VpnService.
	*(*uint16)(unsafe.Pointer(&req.data[0])) = unix.IFF_TUN | unix.IFF_NO_PI
	if err := ioctl(uintptr(fd), unix.TUNSETIFF, unsafe.Pointer(&req)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF: %w", err)
	}
	ifName := string(trimZero(req.name[:]))

	// Дальше настройка адреса — она делается не на самом устройстве, а через
	// обычный сокет: так устроен интерфейс ядра.
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("сокет для настройки: %w", err)
	}
	defer unix.Close(sock)

	set := func(cmd uintptr, fill func(d *[24]byte)) error {
		var r ifreq
		copy(r.name[:], ifName)
		fill(&r.data)
		return ioctl(uintptr(sock), cmd, unsafe.Pointer(&r))
	}
	sockaddr := func(ip []byte) func(d *[24]byte) {
		return func(d *[24]byte) {
			*(*uint16)(unsafe.Pointer(&d[0])) = unix.AF_INET
			copy(d[4:8], ip)
		}
	}

	if err := set(unix.SIOCSIFADDR, sockaddr(addr.To4())); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("задать адрес: %w", err)
	}
	if err := set(unix.SIOCSIFNETMASK, sockaddr(mask)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("задать маску: %w", err)
	}
	if err := set(unix.SIOCSIFMTU, func(d *[24]byte) {
		*(*int32)(unsafe.Pointer(&d[0])) = int32(mtu)
	}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("задать MTU: %w", err)
	}
	if err := set(unix.SIOCSIFFLAGS, func(d *[24]byte) {
		*(*uint16)(unsafe.Pointer(&d[0])) = unix.IFF_UP | unix.IFF_RUNNING
	}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("поднять интерфейс: %w", err)
	}

	// Стек читает дескриптор своим циклом и ждёт неблокирующий режим.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("неблокирующий режим: %w", err)
	}
	return &Tun{FD: fd, Name: ifName}, nil
}

func trimZero(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}
