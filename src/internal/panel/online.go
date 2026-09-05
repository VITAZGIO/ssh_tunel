// Кто сейчас на связи (ТЗ-09) — без внешних программ вроде "ps" или "who",
// прямым обходом /proc: у каждого процесса есть /proc/<pid>/status с его
// uid и /proc/<pid>/comm с именем исполняемого файла. Клиент панели видит
// сервер только через ForceCommand-сессию sshd (см. sshd.go), поэтому
// достаточно посчитать процессы sshd, принадлежащие его uid.
package panel

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcRoot — где искать процессы. В проде main.go передаёт "/proc"; тесты
// указывают на подготовленную временную папку с тем же устройством
// (числовые каталоги + status/comm внутри), не трогая настоящий /proc этой
// машины.
const ProcRoot = "/proc"

// CountUserSessions считает процессы sshd, принадлежащие данному uid, под
// указанным корнем (в проде — ProcRoot). Ошибка чтения отдельного процесса
// (он мог завершиться прямо во время обхода — обычное дело для /proc) не
// прерывает подсчёт остальных: это гонка, а не признак сломанного /proc.
func CountUserSessions(root string, uid int) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue // не числовой каталог — не процесс (self, net, ...)
		}
		procDir := filepath.Join(root, e.Name())
		procUID, ok := readProcUID(procDir)
		if !ok || procUID != uid {
			continue
		}
		if !isSSHDProcess(procDir) {
			continue
		}
		count++
	}
	return count, nil
}

// readProcUID достаёт реальный uid процесса из /proc/<pid>/status — строка
// "Uid:" в этом файле содержит четыре числа (real, effective, saved,
// filesystem); панели нужен первый.
func readProcUID(procDir string) (int, bool) {
	f, err := os.Open(filepath.Join(procDir, "status"))
	if err != nil {
		return 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		uid, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, false
		}
		return uid, true
	}
	return 0, false
}

// isSSHDProcess проверяет имя исполняемого файла процесса. /proc/<pid>/comm
// хранит его без пути и обрезанным до 15 символов ядром — с "sshd" этого
// достаточно, обрезка ему не грозит.
func isSSHDProcess(procDir string) bool {
	data, err := os.ReadFile(filepath.Join(procDir, "comm"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "sshd"
}
