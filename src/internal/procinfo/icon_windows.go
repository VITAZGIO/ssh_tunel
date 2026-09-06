//go:build windows

// Иконка программы рядом с её именем в списке фильтра — то же самое, что
// показывает список процессов в диспетчере задач, только для нашего диалога
// выбора. Windows не даёт иконку файлом — только хендл (HICON), который надо
// самому разобрать по пикселям и собрать в PNG. Стандартный путь для этого:
// SHGetFileInfo достаёт HICON, GetIconInfo — растровые маски из него,
// GetDIBits — сами пиксели.
//
// Любая осечка на этом пути — не повод ломать список: IconPNG возвращает
// ошибку, а обработчик наверху просто не отдаёт картинку. Программа без
// значков работает точно так же, как раньше их не было вовсе.
package procinfo

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"syscall"
	"unsafe"
)

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")
	user32  = syscall.NewLazyDLL("user32.dll")
	gdi32   = syscall.NewLazyDLL("gdi32.dll")

	procSHGetFileInfoW = shell32.NewProc("SHGetFileInfoW")
	procDestroyIcon    = user32.NewProc("DestroyIcon")
	procGetIconInfo    = user32.NewProc("GetIconInfo")
	procGetDC          = user32.NewProc("GetDC")
	procReleaseDC      = user32.NewProc("ReleaseDC")
	procGetDIBits      = gdi32.NewProc("GetDIBits")
	procDeleteObject   = gdi32.NewProc("DeleteObject")
)

const (
	shgfiIcon      = 0x100
	shgfiLargeIcon = 0x0
	shgfiSmallIcon = 0x1
)

type shFileInfoW struct {
	hIcon         syscall.Handle
	iIcon         int32
	dwAttributes  uint32
	szDisplayName [260]uint16
	szTypeName    [80]uint16
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  syscall.Handle
	hbmColor syscall.Handle
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapInfo struct {
	header bitmapInfoHeader
	// Палитра нам не нужна — просим 32 бита на пиксель без сжатия, у такого
	// формата её попросту нет.
}

const dibRGBColors = 0

// IconPNG достаёт иконку исполняемого файла (маленькую, 16×16 — ровно ту, что
// рисуется рядом со строкой в списке) и отдаёт её уже закодированной в PNG.
func IconPNG(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("пустой путь")
	}
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	var info shFileInfoW
	r, _, _ := procSHGetFileInfoW.Call(
		uintptr(unsafe.Pointer(pathPtr)), 0,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info),
		uintptr(shgfiIcon|shgfiSmallIcon))
	if r == 0 || info.hIcon == 0 {
		return nil, errors.New("SHGetFileInfo не вернул значок")
	}
	defer procDestroyIcon.Call(uintptr(info.hIcon))

	img, err := iconToImage(info.hIcon)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// iconToImage разбирает HICON в обычную картинку: GetIconInfo даёт растры
// маски и цвета, GetDIBits читает их как массив пикселей 32bpp BGRA сверху
// вниз (отрицательная высота в заголовке — ровно за этим).
func iconToImage(hIcon syscall.Handle) (image.Image, error) {
	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(uintptr(hIcon), uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil, errors.New("GetIconInfo не сработал")
	}
	defer procDeleteObject.Call(uintptr(ii.hbmMask))
	defer procDeleteObject.Call(uintptr(ii.hbmColor))

	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return nil, errors.New("GetDC не сработал")
	}
	defer procReleaseDC.Call(0, hdc)

	var bi bitmapInfo
	bi.header.biSize = uint32(unsafe.Sizeof(bi.header))
	// Первый вызов — только чтобы GetDIBits заполнил заголовок реальными
	// размерами растра: без него неоткуда узнать, сколько пикселей вообще
	// вычитывать.
	if r, _, _ := procGetDIBits.Call(hdc, uintptr(ii.hbmColor), 0, 0, 0,
		uintptr(unsafe.Pointer(&bi)), dibRGBColors); r == 0 {
		return nil, errors.New("GetDIBits (заголовок) не сработал")
	}
	w := int(bi.header.biWidth)
	h := int(bi.header.biHeight)
	if w <= 0 || h <= 0 || w > 256 || h > 256 {
		return nil, errors.New("неожиданный размер значка")
	}
	bi.header.biHeight = -int32(h) // сверху вниз — иначе строки придут в обратном порядке
	bi.header.biBitCount = 32
	bi.header.biCompression = 0 // BI_RGB

	pixels := make([]byte, w*h*4)
	if r, _, _ := procGetDIBits.Call(hdc, uintptr(ii.hbmColor), 0, uintptr(h),
		uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors); r == 0 {
		return nil, errors.New("GetDIBits (пиксели) не сработал")
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		b := pixels[i*4+0]
		g := pixels[i*4+1]
		r := pixels[i*4+2]
		a := pixels[i*4+3]
		img.Pix[i*4+0] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = a
	}
	return img, nil
}
