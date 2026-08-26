//go:build windows

// Package procinfo определяет, какая программа открыла соединение к нашему
// прокси. Приложение приходит с локального порта (например 127.0.0.1:63675),
// и по этому порту Windows умеет сказать PID владельца сокета, а по PID —
// путь к exe. Именно это позволяет показывать в логе "chrome.exe" вместо
// бессмысленного номера порта.
package procinfo

import (
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi              = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTab = iphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPIDAll = 5
	afInet              = 2
)

type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

type cache struct {
	mu        sync.Mutex
	portToPID map[uint16]uint32
	at        time.Time
	names     map[uint32]string
}

var c = cache{portToPID: map[uint16]uint32{}, names: map[uint32]string{}}

const (
	// staleAfter — через сколько снимок таблицы считается устаревшим.
	staleAfter = 300 * time.Millisecond
	// missRetry — минимальный промежуток между внеплановыми снимками, когда
	// порт в кэше не нашёлся.
	missRetry = 40 * time.Millisecond
)

// refresh обновляет снимок таблицы сокетов. Вызывать только под замком.
func (c *cache) refresh() {
	if m := snapshot(); m != nil {
		c.portToPID = m
		c.at = time.Now()
	}
}

// Lookup возвращает имя процесса, которому принадлежит локальный порт.
// Пустая строка означает "не удалось определить" — это не ошибка: сокет мог
// уже закрыться, или процесс принадлежит другому пользователю.
func Lookup(localPort uint16) (string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Таблица сокетов снимается целиком и стоит недёшево, а соединения при
	// загрузке страницы идут пачками — поэтому кэшируем на короткое время.
	if time.Since(c.at) > staleAfter {
		c.refresh()
	}

	pid, ok := c.portToPID[localPort]
	if !ok {
		// Сокет мог появиться уже после снимка — обновляемся ещё раз, но не
		// чаще, чем раз в missRetry.
		//
		// Без этого ограничения промах стоил полного снимка таблицы, а промах
		// — обычное дело: соединение только что открылось, в прошлом снимке
		// его и не могло быть. На загрузке страницы или тесте скорости это
		// значило десятки снимков подряд, и все под общим замком: каждое новое
		// соединение стояло в очереди за предыдущим. Имя программы того не
		// стоит — без него лог просто скажет «приложение».
		if time.Since(c.at) > missRetry {
			c.refresh()
			pid, ok = c.portToPID[localPort]
		}
		if !ok {
			return "", 0
		}
	}

	if name, ok := c.names[pid]; ok {
		return name, int(pid)
	}
	name := processName(pid)
	if name != "" {
		if len(c.names) > 512 { // не растим кэш имён бесконечно
			c.names = map[uint32]string{}
		}
		c.names[pid] = name
	}
	return name, int(pid)
}

func snapshot() map[uint16]uint32 {
	var size uint32
	// Первый вызов с нулевым буфером сообщает нужный размер.
	procGetExtendedTCPTab.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDAll, 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size+4096) // запас: таблица могла подрасти между вызовами
	size = uint32(len(buf))
	ret, _, _ := procGetExtendedTCPTab.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, afInet, tcpTableOwnerPIDAll, 0,
	)
	if ret != 0 {
		return nil
	}

	n := *(*uint32)(unsafe.Pointer(&buf[0]))
	rowSize := unsafe.Sizeof(mibTCPRowOwnerPID{})
	out := make(map[uint16]uint32, n)
	for i := uint32(0); i < n; i++ {
		off := uintptr(4) + uintptr(i)*rowSize
		if off+rowSize > uintptr(len(buf)) {
			break
		}
		row := (*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[off]))
		// LocalPort лежит в сетевом порядке байт внутри 32-битного поля.
		port := uint16(row.LocalPort&0xff)<<8 | uint16((row.LocalPort>>8)&0xff)
		out[port] = row.OwningPID
	}
	return out
}

func processName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:size]))
}
