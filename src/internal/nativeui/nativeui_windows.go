//go:build windows

// Package nativeui — настоящее окно Windows вместо вкладки браузера.
//
// Внутри окна работает WebView2 (движок Edge, встроен в Windows 10/11), но
// снаружи это обычное приложение: своя иконка, место в панели задач, Alt+Tab,
// значок рядом с часами. Ни адресной строки, ни вкладок, ни возможности
// «потерять» окно среди сотни других вкладок браузера.
//
// Три вещи, которых не давал браузерный вариант, сделаны здесь руками:
//
//   - закрытие окна крестиком прячет его в трей, а не завершает программу
//     (иначе туннель обрывался бы при каждом случайном закрытии);
//   - значок в трее с меню — чтобы отключиться, не открывая окно;
//   - повторный запуск не поднимает вторую копию, а показывает уже
//     работающую (иначе вторая копия просто падала бы на занятых портах).
package nativeui

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Options описывает окно и то, что умеет меню в трее.
type Options struct {
	Title  string
	URL    string
	Width  int // логические пиксели, до поправки на масштаб экрана
	Height int

	// Running сообщает, поднят ли сейчас туннель — от этого зависит текст
	// пункта меню и подсказка у значка.
	Running func() bool
	Toggle  func() // «Подключить»/«Отключить» из меню
	OnQuit  func() // вызывается перед выходом: снять прокси, закрыть туннель

	// DataPath — куда WebView2 складывает свой кэш. По умолчанию он пишет
	// рядом с exe, а если программу положили в Program Files, туда писать
	// нельзя и окно просто не откроется. Поэтому указываем папку настроек.
	DataPath string
}

// Идентификатор иконки в ресурсах exe. Манифест занимает ID 1, группа
// иконок — 2 (см. src/build.sh, генерация rsrc).
const iconResourceID = 2

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	pSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	pCallWindowProc   = user32.NewProc("CallWindowProcW")
	pShowWindow       = user32.NewProc("ShowWindow")
	pSetForegroundWin = user32.NewProc("SetForegroundWindow")
	pCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	pAppendMenu       = user32.NewProc("AppendMenuW")
	pTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	pDestroyMenu      = user32.NewProc("DestroyMenu")
	pGetCursorPos     = user32.NewProc("GetCursorPos")
	pDestroyWindow    = user32.NewProc("DestroyWindow")
	pLoadImage        = user32.NewProc("LoadImageW")
	pSetWindowPos     = user32.NewProc("SetWindowPos")
	pAdjustWindowRect = user32.NewProc("AdjustWindowRectEx")
	pGetDpiForWindow  = user32.NewProc("GetDpiForWindow")
	pFindWindow       = user32.NewProc("FindWindowW")
	pPostMessage      = user32.NewProc("PostMessageW")

	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")

	pShellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")
	pCreateMutex     = kernel32.NewProc("CreateMutexW")
	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmClose      = 0x0010
	wmApp        = 0x8000
	wmTrayCallby = wmApp + 1 // сообщение от значка в трее
	wmShowWindow = wmApp + 2 // «покажись» от второй копии программы

	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205

	// Индексы отрицательные, а принимаются как uintptr — записываем их
	// сразу в дополнительном коде, иначе константа не переводится в uintptr.
	gwlStyle    = ^uintptr(15) // -16
	gwlpWndProc = ^uintptr(3)  // -4

	wsThickFrame  = 0x00040000
	wsMaximizeBox = 0x00010000

	swHide    = 0
	swShow    = 5
	swRestore = 9

	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpFrameChanged = 0x0020

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	mfString    = 0x0000
	mfSeparator = 0x0800

	// Оформление полосы заголовка. Поддерживается начиная с Windows 11.
	dwmUseImmersiveDarkMode = 20
	dwmBorderColor          = 34
	dwmCaptionColor         = 35
	dwmTextColor            = 36

	imageIcon        = 1
	lrDefaultSize    = 0x0040
	lrShared         = 0x8000
	errAlreadyExists = 183
)

// Пункты меню в трее.
const (
	menuOpen = 1001 + iota
	menuToggle
	menuQuit
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

// Windows принимает структуру только целиком нужного размера — при расхождении
// Shell_NotifyIcon молча вернёт ошибку и значка в трее просто не будет.
// Проверяем раскладку на этапе компиляции: если размер не 976 байт (x64),
// вычитание уйдёт в минус и сборка упадёт с понятным местом.
const _ = uint(unsafe.Sizeof(notifyIconData{}) - 976)
const _ = uint(976 - unsafe.Sizeof(notifyIconData{}))

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

type ui struct {
	opts    Options
	view    webview2.WebView
	hwnd    uintptr
	oldProc uintptr
	icon    uintptr
	nid     notifyIconData
	tipMu   sync.Mutex // подсказку трея обновляет посторонняя горутина
	once    sync.Once
}

var active *ui // окно в программе одно, поэтому глобальная ссылка достаточна

// AlreadyRunning возвращает true, если программа уже запущена: тогда вместо
// второй копии надо просто показать окно первой. Мьютекс намеренно не
// освобождается — он живёт до конца процесса.
func AlreadyRunning(title string) bool {
	name, _ := windows.UTF16PtrFromString("Local\\ssh_tunnel_single_instance")
	_, _, err := pCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if errno, ok := err.(windows.Errno); ok && uintptr(errno) == errAlreadyExists {
		showExistingWindow(title)
		return true
	}
	return false
}

func showExistingWindow(title string) {
	class, _ := windows.UTF16PtrFromString("webview")
	name, _ := windows.UTF16PtrFromString(title)

	hwnd, _, _ := pFindWindow.Call(uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(name)))
	if hwnd == 0 {
		// Запасной вариант — искать только по классу окна. Нужен, если рядом
		// работает копия прежней версии с другим заголовком: иначе повторный
		// запуск просто молча ничего бы не сделал.
		hwnd, _, _ = pFindWindow.Call(uintptr(unsafe.Pointer(class)), 0)
	}
	if hwnd != 0 {
		pPostMessage.Call(hwnd, wmShowWindow, 0, 0)
	}
}

// Run создаёт окно и крутит цикл сообщений до выхода из программы.
// Возвращается только когда пользователь выбрал «Выход».
func Run(opts Options) error {
	// Окно и цикл сообщений обязаны жить на одном потоке ОС — Windows
	// доставляет сообщения именно потоку, создавшему окно.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	view := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  opts.DataPath,
		WindowOptions: webview2.WindowOptions{
			Title:  opts.Title,
			Width:  uint(opts.Width),
			Height: uint(opts.Height),
			IconId: iconResourceID,
			Center: true,
		},
	})
	if view == nil {
		return fmt.Errorf("не удалось создать окно (нет компонента WebView2)")
	}

	u := &ui{opts: opts, view: view, hwnd: uintptr(view.Window())}
	active = u

	u.fixWindowStyle()
	u.themeTitleBar()
	u.hookWindowProc()
	u.addTrayIcon()

	view.Navigate(opts.URL)
	view.Run() // крутится, пока не придёт WM_QUIT

	u.removeTrayIcon()
	return nil
}

// fixWindowStyle убирает возможность растягивать и разворачивать окно: у
// приложения фиксированный вертикальный формат, и растянутое на весь экран
// оно выглядит нелепо. Заодно приводит РАБОЧУЮ область к нужному размеру —
// CreateWindow задаёт внешний размер вместе с рамками, из-за чего содержимое
// оказалось бы меньше задуманного.
func (u *ui) fixWindowStyle() {
	style, _, _ := pGetWindowLongPtr.Call(u.hwnd, gwlStyle)
	style &^= wsThickFrame | wsMaximizeBox
	pSetWindowLongPtr.Call(u.hwnd, gwlStyle, style)

	// Масштаб экрана: на мониторе со 150% окно в физических пикселях должно
	// быть в полтора раза больше, иначе оно выйдет крошечным.
	dpi := uintptr(96)
	if pGetDpiForWindow.Find() == nil {
		if d, _, _ := pGetDpiForWindow.Call(u.hwnd); d > 0 {
			dpi = d
		}
	}
	w := int32(u.opts.Width) * int32(dpi) / 96
	h := int32(u.opts.Height) * int32(dpi) / 96

	r := rect{0, 0, w, h}
	pAdjustWindowRect.Call(uintptr(unsafe.Pointer(&r)), style, 0, 0)

	pSetWindowPos.Call(u.hwnd, 0, 0, 0,
		uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top),
		swpNoMove|swpNoZOrder|swpFrameChanged)
}

// themeTitleBar красит полосу заголовка в цвет фона приложения, чтобы окно
// выглядело цельным, а не «тёмная программа в светлой рамке».
//
// Работает на Windows 11 (сборка 22000 и новее). На более старых системах
// вызовы просто возвращают ошибку и ничего не меняют — заголовок остаётся
// системным, и это не мешает работе.
func (u *ui) themeTitleBar() {
	dark := systemUsesDarkTheme()

	// COLORREF — это 0x00BBGGRR, то есть байты идут в обратном порядке
	// относительно привычной записи цвета #RRGGBB.
	var caption, text uint32
	if dark {
		caption, text = 0x17100d, 0xf2ebe8 // фон #0d1017, текст #e8ebf2
	} else {
		caption, text = 0xf6f1ee, 0x211814 // фон #eef1f6, текст #141821
	}

	setAttr := func(attr uint32, v uint32) {
		pDwmSetWindowAttribute.Call(u.hwnd, uintptr(attr),
			uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
	}

	// Тёмный режим переключает отрисовку самих кнопок свернуть/развернуть/
	// закрыть: без него они остаются чёрными на тёмном фоне и не видны.
	var darkFlag uint32
	if dark {
		darkFlag = 1
	}
	setAttr(dwmUseImmersiveDarkMode, darkFlag)
	setAttr(dwmCaptionColor, caption)
	setAttr(dwmTextColor, text)
	setAttr(dwmBorderColor, caption)
}

// systemUsesDarkTheme читает выбранную пользователем тему приложений: окно
// красится под неё, потому что и сам интерфейс подстраивается под тему.
func systemUsesDarkTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true // тёмная — оформление приложения по умолчанию
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return true
	}
	return v == 0
}

// hookWindowProc подменяет оконную процедуру, чтобы перехватить закрытие и
// сообщения от значка в трее. Всё, что нас не касается, отдаём обратно
// библиотеке — она обслуживает сам WebView2.
func (u *ui) hookWindowProc() {
	proc := windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
		switch msg {
		case wmClose:
			// Крестик прячет окно, а не закрывает программу: туннель должен
			// пережить случайное закрытие. Выход — только через меню в трее.
			u.hide()
			return 0

		case wmShowWindow:
			u.show()
			return 0

		case wmTrayCallby:
			switch lparam & 0xFFFF {
			case wmLButtonUp, wmLButtonDblClk:
				u.show()
			case wmRButtonUp:
				u.showMenu()
			}
			return 0
		}
		r, _, _ := pCallWindowProc.Call(u.oldProc, hwnd, msg, wparam, lparam)
		return r
	})
	u.oldProc, _, _ = pSetWindowLongPtr.Call(u.hwnd, gwlpWndProc, proc)
}

func (u *ui) show() {
	pShowWindow.Call(u.hwnd, swShow)
	pShowWindow.Call(u.hwnd, swRestore)
	pSetForegroundWin.Call(u.hwnd)
}

func (u *ui) hide() {
	pShowWindow.Call(u.hwnd, swHide)
}

// showMenu показывает меню у значка в трее.
func (u *ui) showMenu() {
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)

	appendItem(menu, menuOpen, "Открыть")
	toggle := "Подключить"
	if u.opts.Running != nil && u.opts.Running() {
		toggle = "Отключить"
	}
	appendItem(menu, menuToggle, toggle)
	pAppendMenu.Call(menu, mfSeparator, 0, 0)
	appendItem(menu, menuQuit, "Выход")

	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// Без этого меню не закроется по щелчку мимо него — известная особенность
	// меню, вызванных из значка в трее.
	pSetForegroundWin.Call(u.hwnd)

	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd,
		uintptr(pt.X), uintptr(pt.Y), 0, u.hwnd, 0)

	switch cmd {
	case menuOpen:
		u.show()
	case menuToggle:
		if u.opts.Toggle != nil {
			go u.opts.Toggle()
		}
	case menuQuit:
		u.quit()
	}
}

func appendItem(menu uintptr, id int, text string) {
	s, _ := windows.UTF16PtrFromString(text)
	pAppendMenu.Call(menu, mfString, uintptr(id), uintptr(unsafe.Pointer(s)))
}

func (u *ui) quit() {
	u.once.Do(func() {
		if u.opts.OnQuit != nil {
			u.opts.OnQuit() // снять системный прокси до закрытия окна
		}
		u.removeTrayIcon()
		pDestroyWindow.Call(u.hwnd) // дальше библиотека пошлёт WM_QUIT
	})
}

func (u *ui) addTrayIcon() {
	hinst, _, _ := pGetModuleHandle.Call(0)
	u.icon, _, _ = pLoadImage.Call(hinst, iconResourceID, imageIcon, 0, 0, lrDefaultSize|lrShared)

	u.nid = notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             u.hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTrayCallby,
		HIcon:            u.icon,
	}
	copyTip(&u.nid, "ssh_tunnel")
	pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&u.nid)))
}

func (u *ui) removeTrayIcon() {
	pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&u.nid)))
}

func copyTip(nid *notifyIconData, text string) {
	s, err := windows.UTF16FromString(text)
	if err != nil {
		return
	}
	if len(s) > len(nid.SzTip) {
		s = s[:len(nid.SzTip)-1]
		s[len(s)-1] = 0
	}
	copy(nid.SzTip[:], s)
}

// SetStatus обновляет подсказку у значка в трее, чтобы состояние было видно,
// не открывая окно.
//
// Заголовок окна намеренно НЕ меняется: по нему вторая копия программы ищет
// окно первой (см. showExistingWindow), и «плавающий» заголовок сломал бы этот
// поиск ровно тогда, когда туннель подключён.
func SetStatus(text string) {
	u := active
	if u == nil || u.hwnd == 0 {
		return
	}
	u.tipMu.Lock()
	copyTip(&u.nid, "ssh_tunnel — "+text)
	pShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&u.nid)))
	u.tipMu.Unlock()
}

// WebView2Installed сообщает, есть ли в системе компонент, на котором рисуется
// окно. На Windows 10/11 он ставится вместе с Edge, но у сильно урезанных
// сборок его может не быть — тогда лучше сказать об этом прямо.
func WebView2Installed() bool {
	v, err := webviewloader.GetInstalledVersion()
	return err == nil && v != ""
}
