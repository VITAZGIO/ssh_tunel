//go:build ignore

// Генератор иконки приложения. Рисует щит с точкой и дугами (соединение под
// защитой) и складывает несколько размеров в один .ico.
//
// Иконка сделана кодом, а не картинкой из редактора, чтобы её можно было
// пересобрать одной командой и не тащить в репозиторий бинарный файл
// непонятного происхождения:
//
//	go run tools/mkicon/main.go internal/nativeui/icon.ico
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
)

// Размеры, которые Windows реально спрашивает: трей, панель задач, крупные
// значки в проводнике и Alt+Tab.
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	out := "icon.ico"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	var images [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, drawIcon(s)); err != nil {
			panic(err)
		}
		images = append(images, buf.Bytes())
	}

	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	writeICO(f, images)
}

// drawIcon рисует щит со скруглёнными краями и «сигналом» внутри.
func drawIcon(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), image.Transparent, image.Point{}, draw.Src)

	s := float64(size)
	// Сглаживание делаем честной суперсэмплингом: считаем покрытие пикселя
	// по сетке 4x4 — на мелких размерах это единственное, что даёт читаемость.
	const ss = 4
	cx, cy := s/2, s/2

	blue := color.RGBA{0x4c, 0x8d, 0xff, 0xff}
	green := color.RGBA{0x2f, 0xbf, 0x71, 0xff}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var shield, mark float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					if inShield(px, py, s) {
						shield++
						if inMark(px-cx, py-cy, s) {
							mark++
						}
					}
				}
			}
			total := float64(ss * ss)
			if shield == 0 {
				continue
			}
			a := shield / total
			m := mark / total

			// Лёгкий вертикальный градиент — иконка перестаёт выглядеть плоской.
			t := float64(y) / s
			base := color.RGBA{
				R: uint8(float64(blue.R)*(1-t*0.35)) ,
				G: uint8(float64(blue.G)*(1-t*0.18)),
				B: uint8(float64(blue.B)*(1-t*0.05)),
				A: 255,
			}
			c := blend(base, green, m/max(a, 0.0001))
			img.Set(x, y, color.RGBA{c.R, c.G, c.B, uint8(a * 255)})
		}
	}
	return img
}

// inShield — форма щита: прямоугольник со скруглённым верхом и сходящимся низом.
func inShield(x, y, s float64) bool {
	pad := s * 0.11
	w := s - 2*pad
	nx := (x - pad) / w // 0..1
	ny := (y - pad) / (s - 2*pad)
	if nx < 0 || nx > 1 || ny < 0 || ny > 1 {
		return false
	}
	dx := math.Abs(nx-0.5) * 2 // 0 в центре, 1 у края

	if ny < 0.62 {
		// Верх — прямоугольник со скруглёнными углами.
		r := 0.18
		if ny < r && dx > 1-r*2 {
			ex := (dx - (1 - r*2)) / (r * 2)
			ey := (r - ny) / r
			return ex*ex+ey*ey <= 1
		}
		return true
	}
	// Низ — плавное схождение к острию.
	k := (ny - 0.62) / 0.38
	return dx <= 1-k*k
}

// inMark — «сигнал» внутри щита: точка и две дуги, расходящиеся вправо-вверх.
func inMark(dx, dy, s float64) bool {
	dy += s * 0.03 // визуальный центр щита выше геометрического
	r := math.Hypot(dx, dy)

	if r <= s*0.075 { // точка
		return true
	}
	// Дуги рисуем только в верхнем секторе, иначе получается мишень.
	ang := math.Atan2(-dy, dx)
	if ang < -0.25 || ang > math.Pi/2+0.25 {
		return false
	}
	for _, k := range []float64{0.19, 0.30} {
		if math.Abs(r-s*k) <= s*0.036 {
			return true
		}
	}
	return false
}

func blend(a, b color.RGBA, t float64) color.RGBA {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return color.RGBA{
		R: uint8(float64(a.R)*(1-t) + float64(b.R)*t),
		G: uint8(float64(a.G)*(1-t) + float64(b.G)*t),
		B: uint8(float64(a.B)*(1-t) + float64(b.B)*t),
		A: 255,
	}
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
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
