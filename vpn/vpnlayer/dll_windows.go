package vpnlayer

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"sshtunnel/internal/config"
)

// wintunFiles — папка с драйвером Wintun (подписан WireGuard LLC), вшитая в
// exe, чтобы программа оставалась одним файлом. Самого драйвера в репозитории
// нет: его скачивает build.sh с wintun.net и сверяет контрольную сумму. Папка,
// а не файл, — чтобы код проверялся и собирался и без драйвера.
//
//go:embed wintun
var wintunFiles embed.FS

// loadWintun кладёт драйвер в папку настроек и загружает его по полному пути.
//
// Библиотека WireGuard ищет «wintun.dll» по имени — рядом с exe и в System32.
// Но если модуль с таким именем уже загружен в процесс, Windows отдаёт его,
// никуда не заглядывая. Поэтому достаточно загрузить его первыми, откуда
// удобно нам, — и не мусорить рядом с exe, который может лежать где угодно.
func loadWintun() error {
	wintunDLL, _ := wintunFiles.ReadFile("wintun/wintun.dll")
	// Настоящий драйвер — PE-файл в сотни килобайт. Заглушка на его месте
	// значит, что exe собран без build.sh.
	if len(wintunDLL) < 64*1024 || !bytes.HasPrefix(wintunDLL, []byte("MZ")) {
		return fmt.Errorf("программа собрана без драйвера Wintun — собери её через vpn/build.sh")
	}
	dir := filepath.Join(config.Dir(), "wintun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("папка для драйвера: %w", err)
	}
	path := filepath.Join(dir, "wintun.dll")
	if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, wintunDLL) {
		// Загруженный прошлым запуском файл Windows держит открытым, пока
		// процесс жив, — но этот процесс только начался, так что перезапись
		// проходит. Сначала во временный файл: оборванная запись не должна
		// оставить полдрайвера.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, wintunDLL, 0o600); err != nil {
			return fmt.Errorf("записать драйвер: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("записать драйвер: %w", err)
		}
	}
	if _, err := windows.LoadLibraryEx(path, 0, windows.LOAD_WITH_ALTERED_SEARCH_PATH); err != nil {
		return fmt.Errorf("загрузить драйвер Wintun: %w", err)
	}
	return nil
}
