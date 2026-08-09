//go:build linux

// Определение программы, открывшей соединение, для Linux.
//
// Здесь нет системного вызова «дай владельца порта», как в Windows, поэтому
// делается в два шага, как это делает netstat: сначала из /proc/net/tcp
// берётся номер сокета (inode) для нужного локального порта, затем по
// /proc/<pid>/fd ищется процесс, у которого этот сокет открыт.
//
// Второй шаг дорогой — это обход всех процессов и всех их дескрипторов.
// Поэтому результат кэшируется на короткое время: при загрузке страницы
// соединения идут пачками, и без кэша программа тратила бы больше времени на
// поиск имени, чем на сам трафик.
package procinfo

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type procCache struct {
	mu       sync.Mutex
	inodePID map[string]int // inode сокета -> pid
	names    map[int]string // pid -> имя программы
	at       time.Time
}

var pc = procCache{inodePID: map[string]int{}, names: map[int]string{}}

// Lookup возвращает имя программы, которой принадлежит локальный порт.
func Lookup(localPort uint16) (string, int) {
	inode := inodeForPort(localPort)
	if inode == "" {
		return "", 0
	}

	pc.mu.Lock()
	defer pc.mu.Unlock()

	if time.Since(pc.at) > 2*time.Second {
		pc.inodePID, pc.names = scanProcesses()
		pc.at = time.Now()
	}
	pid, ok := pc.inodePID[inode]
	if !ok {
		// Сокет мог открыться уже после обхода — обновляемся один раз.
		pc.inodePID, pc.names = scanProcesses()
		pc.at = time.Now()
		if pid, ok = pc.inodePID[inode]; !ok {
			return "", 0
		}
	}
	return pc.names[pid], pid
}

// inodeForPort ищет номер сокета по локальному порту в таблицах ядра.
func inodeForPort(port uint16) string {
	target := strings.ToUpper(strconv.FormatUint(uint64(port), 16))
	if len(target) < 4 {
		target = strings.Repeat("0", 4-len(target)) + target
	}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// local_address имеет вид "0100007F:1F90" — адрес и порт.
			if i := strings.LastIndex(fields[1], ":"); i >= 0 && fields[1][i+1:] == target {
				return fields[9] // inode
			}
		}
	}
	return ""
}

// scanProcesses обходит /proc и собирает соответствие «сокет -> процесс».
func scanProcesses() (map[string]int, map[int]string) {
	inodePID := map[string]int{}
	names := map[int]string{}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return inodePID, names
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // не процесс
		}
		fdDir := filepath.Join("/proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // чужой процесс, читать не дают — это нормально
		}
		var name string
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			inodePID[inode] = pid
			if name == "" {
				name = processName(pid)
			}
		}
		if name != "" {
			names[pid] = name
		}
	}
	return inodePID, names
}

func processName(pid int) string {
	// comm короче и всегда есть; exe даёт полный путь, но не для чужих процессов.
	if b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm")); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// Process — запущенная программа для списка выбора.
type Process struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// List перечисляет запущенные программы. Повторы схлопываются: у браузера
// бывает по десятку процессов с одним именем, а правило всё равно задаётся по
// имени.
func List() []Process {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	seen := map[string]*Process{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		name := processName(pid)
		if name == "" || isKernelThread(pid) {
			continue
		}
		key := strings.ToLower(name)
		p, ok := seen[key]
		if !ok {
			p = &Process{Name: name}
			seen[key] = p
		}
		if p.Path == "" {
			if link, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe")); err == nil {
				p.Path = link
			}
		}
	}
	out := make([]Process, 0, len(seen))
	for _, p := range seen {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// isKernelThread отсеивает потоки ядра: у них нет исполняемого файла, и в
// списке выбора программ им делать нечего.
func isKernelThread(pid int) bool {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	return err != nil || len(strings.TrimRight(string(b), "\x00")) == 0
}
