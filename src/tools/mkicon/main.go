//go:build ignore

// Собирает иконку приложения (.ico) из одной исходной картинки: уменьшает её
// до всех размеров, которые спрашивает Windows, и складывает в один файл.
//
// Сделано инструментом, а не готовым .ico из редактора, чтобы иконку можно
// было пересобрать одной командой, а в репозитории лежал понятный исходник:
//
//	go run tools/mkicon/main.go internal/nativeui/logo.png internal/nativeui/icon.ico
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

	var images [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, resize(src, s)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		images = append(images, buf.Bytes())
	}

	f, err := os.Create(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	writeICO(f, images)
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
// достаточно и результат чище, чем у выборки ближайшего пикселя: логотип
// состоит из тонких диагоналей, которые иначе рассыпались бы на мелких
// размерах.
//
// Усреднять надо ЦВЕТ, УМНОЖЕННЫЙ НА ПРОЗРАЧНОСТЬ, иначе по краям вылезает
// тёмная кайма из полностью прозрачных пикселей.
func resize(src image.Image, size int) image.Image {
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

			var sr, sg, sb, sa uint64
			var n uint64
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

// writeICO собирает контейнер .ico из готовых PNG. Начиная с Vista Windows
// понимает PNG внутри ico, поэтому возиться с форматом BMP не нужно.
func writeICO(f *os.File, images [][]byte) {
	n := len(images)
	binary.Write(f, binary.LittleEndian, uint16(0)) // зарезервировано
	binary.Write(f, binary.LittleEndian, uint16(1)) // тип: иконка
	binary.Write(f, binary.LittleEndian, uint16(n))

	offset := 6 + 16*n
	for i, img := range images {
		s := sizes[i]
		b := byte(s)
		if s >= 256 {
			b = 0 // 256 кодируется нулём
		}
		f.Write([]byte{b, b, 0, 0})
		binary.Write(f, binary.LittleEndian, uint16(1))  // плоскости
		binary.Write(f, binary.LittleEndian, uint16(32)) // бит на пиксель
		binary.Write(f, binary.LittleEndian, uint32(len(img)))
		binary.Write(f, binary.LittleEndian, uint32(offset))
		offset += len(img)
	}
	for _, img := range images {
		f.Write(img)
	}
}
