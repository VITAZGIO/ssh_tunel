//go:build ignore

// Собирает иконку приложения (.ico) из одной исходной картинки: уменьшает её
// до всех размеров, которые спрашивает Windows, и складывает в один файл.
//
//	go run tools/mkicon/main.go internal/nativeui/icon-source.png internal/nativeui/icon.ico
//
// Важная деталь про формат. Внутри .ico каждая картинка лежит либо как PNG,
// либо как DIB (несжатый растр Windows). PNG внутри иконок понимают начиная с
// Vista, но НЕ везде: часть механизмов оболочки — панель задач, Alt+Tab,
// значки в проводнике — для размеров меньше 256 ждут именно DIB и на PNG
// показывают старую иконку из кэша или вообще ничего. Поэтому здесь всё до
// 256 пишется как DIB, и только 256 — как PNG (иначе он раздул бы файл).
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// Размеры, которые Windows реально спрашивает: трей и мелкие списки (16),
// панель задач (24, 32), крупные значки проводника (48, 64), Alt+Tab и
// плитки (128, 256).
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "использование: mkicon <исходный.png> <результат.ico>")
		os.Exit(2)
	}
	src, err := loadPNG(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "не могу прочитать исходник:", err)
		os.Exit(1)
	}

	type entry struct {
		size int
		data []byte
	}
	var entries []entry
	for _, s := range sizes {
		img := resize(src, s)
		var data []byte
		if s >= 256 {
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			data = buf.Bytes()
		} else {
			data = encodeDIB(img)
		}
		entries = append(entries, entry{s, data})
	}

	f, err := os.Create(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	n := len(entries)
	binary.Write(f, binary.LittleEndian, uint16(0)) // зарезервировано
	binary.Write(f, binary.LittleEndian, uint16(1)) // тип: иконка
	binary.Write(f, binary.LittleEndian, uint16(n))

	offset := 6 + 16*n
	for _, e := range entries {
		b := byte(e.size)
		if e.size >= 256 {
			b = 0 // 256 кодируется нулём
		}
		f.Write([]byte{b, b, 0, 0})
		binary.Write(f, binary.LittleEndian, uint16(1))  // плоскости
		binary.Write(f, binary.LittleEndian, uint16(32)) // бит на пиксель
		binary.Write(f, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(f, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range entries {
		f.Write(e.data)
	}

	fmt.Printf("иконка собрана: %d размеров, %d байт\n", n, offset)
}

// encodeDIB пишет картинку в том виде, в каком иконку ждёт Windows:
// заголовок BITMAPINFOHEADER, затем пиксели BGRA снизу вверх, затем маска
// прозрачности. Маска нулевая — прозрачность берётся из альфа-канала, но сама
// маска обязана присутствовать, иначе картинка считается битой.
func encodeDIB(img *image.NRGBA) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var out bytes.Buffer

	// BITMAPINFOHEADER. Высота удвоена: формат считает, что за картинкой идёт
	// маска той же высоты.
	binary.Write(&out, binary.LittleEndian, uint32(40))
	binary.Write(&out, binary.LittleEndian, int32(w))
	binary.Write(&out, binary.LittleEndian, int32(h*2))
	binary.Write(&out, binary.LittleEndian, uint16(1))  // плоскости
	binary.Write(&out, binary.LittleEndian, uint16(32)) // бит на пиксель
	binary.Write(&out, binary.LittleEndian, uint32(0))  // без сжатия
	binary.Write(&out, binary.LittleEndian, uint32(w*h*4))
	binary.Write(&out, binary.LittleEndian, int32(0)) // разрешение по X
	binary.Write(&out, binary.LittleEndian, int32(0)) // разрешение по Y
	binary.Write(&out, binary.LittleEndian, uint32(0))
	binary.Write(&out, binary.LittleEndian, uint32(0))

	// Пиксели: снизу вверх, порядок каналов B, G, R, A.
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(b.Min.X+x, b.Min.Y+y)
			out.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}

	// Маска: по биту на пиксель, строки выровнены на 4 байта.
	rowBytes := ((w + 31) / 32) * 4
	mask := make([]byte, rowBytes*h)
	out.Write(mask)

	return out.Bytes()
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// resize уменьшает картинку усреднением по площади. Для уменьшения этого
// достаточно и результат чище, чем у выборки ближайшего пикселя: остриё щита
// и прорезь скважины иначе рассыпаются на мелких размерах.
//
// Усреднять надо ЦВЕТ, УМНОЖЕННЫЙ НА ПРОЗРАЧНОСТЬ, иначе по краям вылезает
// тёмная кайма из полностью прозрачных пикселей.
func resize(src image.Image, size int) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			x0 := b.Min.X + x*b.Dx()/size
			x1 := b.Min.X + (x+1)*b.Dx()/size
			y0 := b.Min.Y + y*b.Dy()/size
			y1 := b.Min.Y + (y+1)*b.Dy()/size
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}

			var sr, sg, sb, sa, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					r, g, bl, a := src.At(xx, yy).RGBA() // уже умножены на альфу
					sr += uint64(r)
					sg += uint64(g)
					sb += uint64(bl)
					sa += uint64(a)
					n++
				}
			}
			if n == 0 || sa == 0 {
				continue
			}
			a := sa / n
			// Возвращаемся к цвету без домножения на прозрачность.
			dst.Set(x, y, color.NRGBA{
				R: uint8(sr / n * 255 / a),
				G: uint8(sg / n * 255 / a),
				B: uint8(sb / n * 255 / a),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}
