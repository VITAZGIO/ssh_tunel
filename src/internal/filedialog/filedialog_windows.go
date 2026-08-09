//go:build windows

// Package filedialog открывает обычный системный диалог выбора файла.
//
// Нужен для одного: выбрать программу с диска, если её нет среди запущенных.
// Веб-страница внутри окна сделать этого не может — доступ к файловой системе
// у неё намеренно закрыт, поэтому диалог показывает нативная часть.
package filedialog

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	comdlg32            = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileName = comdlg32.NewProc("GetOpenFileNameW")
	procCommDlgError    = comdlg32.NewProc("CommDlgExtendedError")
)

type openFileName struct {
	StructSize      uint32
	Owner           uintptr
	Instance        uintptr
	Filter          *uint16
	CustomFilter    *uint16
	MaxCustomFilter uint32
	FilterIndex     uint32
	File            *uint16
	MaxFile         uint32
	FileTitle       *uint16
	MaxFileTitle    uint32
	InitialDir      *uint16
	Title           *uint16
	Flags           uint32
	FileOffset      uint16
	FileExtension   uint16
	DefExt          *uint16
	CustData        uintptr
	FnHook          uintptr
	TemplateName    *uint16
	PvReserved      uintptr
	DwReserved      uint32
	FlagsEx         uint32
}

// Структуру Windows принимает только целиком нужного размера — при
// расхождении диалог просто не откроется. Проверяем раскладку на этапе
// компиляции, чтобы ошибка вылезла при сборке, а не у пользователя.
const _ = uint(unsafe.Sizeof(openFileName{}) - 152)
const _ = uint(152 - unsafe.Sizeof(openFileName{}))

const (
	ofnPathMustExist = 0x00000800
	ofnFileMustExist = 0x00001000
	ofnNoChangeDir   = 0x00000008
	ofnExplorer      = 0x00080000
)

// ErrCancelled — пользователь закрыл диалог, ничего не выбрав. Это не сбой, и
// интерфейс не должен показывать из-за этого ошибку.
var ErrCancelled = errors.New("выбор отменён")

// PickExecutable показывает диалог выбора программы и возвращает полный путь.
func PickExecutable() (string, error) {
	// Диалог заводит собственный цикл сообщений и должен жить на одном потоке
	// ОС от начала до конца.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Фильтр — пары «описание, маска», всё через нули и с двумя нулями в конце.
	filter := utf16List("Программы (*.exe)", "*.exe", "Все файлы (*.*)", "*.*")
	title, _ := windows.UTF16PtrFromString("Выбери программу")

	buf := make([]uint16, windows.MAX_PATH*4)
	ofn := openFileName{
		StructSize:  uint32(unsafe.Sizeof(openFileName{})),
		Filter:      &filter[0],
		FilterIndex: 1,
		File:        &buf[0],
		MaxFile:     uint32(len(buf)),
		Title:       title,
		Flags:       ofnPathMustExist | ofnFileMustExist | ofnNoChangeDir | ofnExplorer,
	}

	ret, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		// Ноль означает и отмену, и ошибку — различаются они только этим
		// вызовом. Отмену наружу отдаём отдельной ошибкой.
		if code, _, _ := procCommDlgError.Call(); code == 0 {
			return "", ErrCancelled
		} else {
			return "", errors.New("не удалось открыть диалог выбора файла")
		}
	}
	return windows.UTF16ToString(buf), nil
}

// utf16List собирает строки в один буфер, разделённый нулями, с двумя нулями
// в конце — в таком виде Windows ждёт список фильтров.
func utf16List(parts ...string) []uint16 {
	var out []uint16
	for _, p := range parts {
		s, err := windows.UTF16FromString(p)
		if err != nil {
			continue
		}
		out = append(out, s...) // UTF16FromString уже добавляет нуль
	}
	return append(out, 0)
}
