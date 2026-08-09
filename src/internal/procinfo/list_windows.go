//go:build windows

package procinfo

import (
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process — запущенная программа для списка выбора.
type Process struct {
	Name string `json:"name"` // chrome.exe
	Path string `json:"path"` // полный путь, если удалось узнать
}

// List перечисляет запущенные программы — как диспетчер задач, только без
// системного мусора и без повторов.
//
// Одинаковые программы схлопываются: у браузера бывает по десятку процессов с
// одним именем, и показывать их каждый по отдельности бессмысленно — правило
// фильтра всё равно задаётся по имени.
func List() []Process {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil
	}

	seen := map[string]*Process{}
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if name != "" && !isSystemNoise(name) {
			key := strings.ToLower(name)
			p, ok := seen[key]
			if !ok {
				p = &Process{Name: name}
				seen[key] = p
			}
			// Путь узнаём только один раз на имя: открывать процесс недёшево,
			// а для списка достаточно любого экземпляра.
			if p.Path == "" {
				p.Path = pathOf(entry.ProcessID)
			}
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
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

func pathOf(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "" // системные процессы не открываются, и это нормально
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// isSystemNoise отсеивает то, что в списке фильтра только мешает: службы и
// вспомогательные процессы самой Windows, которые пользователь никогда не
// станет выбирать осознанно.
func isSystemNoise(name string) bool {
	switch strings.ToLower(filepath.Base(name)) {
	case "system", "system idle process", "registry", "memory compression",
		"smss.exe", "csrss.exe", "wininit.exe", "services.exe", "lsass.exe",
		"fontdrvhost.exe", "dwm.exe", "sihost.exe", "ctfmon.exe",
		"runtimebroker.exe", "dllhost.exe", "wmiprvse.exe", "conhost.exe",
		"searchindexer.exe", "audiodg.exe", "spoolsv.exe", "taskhostw.exe",
		"secure system", "lsaiso.exe", "winlogon.exe":
		return true
	}
	return false
}
