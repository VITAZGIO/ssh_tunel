//go:build ignore

// Рисует варианты иконки приложения: щит в цвет логотипа со сквозной
// замочной скважиной, фон прозрачный.
//
//	go run tools/mkshield/main.go <папка-результата>
//
// Все щиты здесь — с ОСТРЫМИ углами: контур задаётся многоугольником, и
// скруглений нигде нет. Различаются силуэты: пятиугольный значок, чистый
// треугольник, классический геральдический с выпуклыми боками, со скошенными
// плечами, норманнский с изломом и с вогнутыми боками.
//
// Каждый вариант сохраняется в 512 и в 32 пикселя — второй нужен, чтобы сразу
// видеть, не разваливается ли рисунок в размере значка в трее. Выбранный
// вариант потом превращается в .ico инструментом mkicon.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Цвет логотипа VG.
var cyan = color.RGBA{0x2d, 0xe2, 0xff, 0xff}

type pt struct{ x, y float64 }

type variant struct {
	name   string
	desc   string
	shape  []pt
	keyCY  float64 // центр скважины по вертикали — зависит от силуэта
	keyLen float64 // длина прорези
}

// Общие границы силуэта, чтобы все варианты были одного размера и веса.
const (
	top    = 0.055
	bottom = 0.965
	left   = 0.085
	right  = 0.915
)

func main() {
	out := "."
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}

	for _, v := range variants() {
		for _, size := range []int{512, 32} {
			f, err := os.Create(filepath.Join(out, fmt.Sprintf("%s-%d.png", v.name, size)))
			if err != nil {
				panic(err)
			}
			if err := png.Encode(f, render(v, size)); err != nil {
				panic(err)
			}
			f.Close()
		}
		fmt.Printf("%-18s %s\n", v.name, v.desc)
	}
}

func variants() []variant {
	return []variant{
		{
			name: "1-pyatiugolnyy",
			desc: "пятиугольный значок: прямые плечи, две грани к острию",
			shape: []pt{
				{left, top}, {right, top},
				{right, 0.56}, {0.5, bottom}, {left, 0.56},
			},
			keyCY: 0.36, keyLen: 0.21,
		},
		{
			name: "2-treugolnyy",
			desc: "треугольный: плоский верх и прямые грани в остриё",
			shape: []pt{
				{left, top}, {right, top}, {0.5, bottom},
			},
			keyCY: 0.30, keyLen: 0.19,
		},
		{
			name: "3-geraldicheskiy",
			desc: "классический геральдический: прямые плечи, выпуклые бока",
			shape: append(append([]pt{{left, top}, {right, top}, {right, 0.42}},
				curve(pt{right, 0.42}, pt{0.885, 0.80}, pt{0.5, bottom})...),
				curve(pt{0.5, bottom}, pt{0.115, 0.80}, pt{left, 0.42})...),
			keyCY: 0.35, keyLen: 0.21,
		},
		{
			name: "4-so-skosom",
			desc: "со скошенными верхними углами",
			shape: []pt{
				{0.24, top}, {0.76, top}, {right, 0.17},
				{right, 0.54}, {0.5, bottom}, {left, 0.54}, {left, 0.17},
			},
			keyCY: 0.37, keyLen: 0.21,
		},
		{
			name: "5-normannskiy",
			desc: "норманнский: бока с изломом, длинное остриё",
			shape: []pt{
				{left, top}, {right, top},
				{right, 0.46}, {0.79, 0.72}, {0.5, bottom}, {0.21, 0.72}, {left, 0.46},
			},
			keyCY: 0.34, keyLen: 0.20,
		},
		{
			name: "6-vognutyy",
			desc: "с вогнутыми боками, узкое остриё",
			shape: append(append([]pt{{left, top}, {right, top}, {right, 0.34}},
				curve(pt{right, 0.34}, pt{0.70, 0.62}, pt{0.5, bottom})...),
				curve(pt{0.5, bottom}, pt{0.30, 0.62}, pt{left, 0.34})...),
			keyCY: 0.32, keyLen: 0.19,
		},
	}
}

// curve разбивает квадратичную кривую на отрезки — так изогнутый бок можно
// хранить тем же многоугольником, что и прямые грани, и не заводить отдельную
// математику для заливки.
func curve(a, ctrl, b pt) []pt {
	const steps = 48
	out := make([]pt, 0, steps)
	for i := 1; i <= steps; i++ {
		t := float64(i) / steps
		u := 1 - t
		out = append(out, pt{
			x: u*u*a.x + 2*u*t*ctrl.x + t*t*b.x,
			y: u*u*a.y + 2*u*t*ctrl.y + t*t*b.y,
		})
	}
	return out
}

// render рисует вариант со сглаживанием: покрытие пикселя считается по сетке
// 4x4. Без этого острые углы и прорезь скважины рвутся уже на 64 пикселях.
func render(v variant, size int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	s := float64(size)

	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			hits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / s
					y := (float64(py) + (float64(sy)+0.5)/ss) / s
					if inPoly(v.shape, x, y) && !inKeyhole(v, x, y) {
						hits++
					}
				}
			}
			if hits == 0 {
				continue
			}
			a := float64(hits) / float64(ss*ss)
			img.Set(px, py, color.NRGBA{cyan.R, cyan.G, cyan.B, uint8(a*255 + 0.5)})
		}
	}
	return img
}

// inPoly — принадлежность точки многоугольнику методом испускания луча.
func inPoly(p []pt, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		if (p[i].y > y) != (p[j].y > y) &&
			x < (p[j].x-p[i].x)*(y-p[i].y)/(p[j].y-p[i].y)+p[i].x {
			in = !in
		}
	}
	return in
}

// inKeyhole — замочная скважина: круг и расширяющаяся книзу прорезь.
// Прорезь именно расширяется: сужающаяся к низу в размере значка в трее
// схлопывается в точку и перестаёт читаться.
func inKeyhole(v variant, x, y float64) bool {
	const cx = 0.5
	const r = 0.105
	dx, dy := x-cx, y-v.keyCY

	if math.Hypot(dx, dy) <= r {
		return true
	}
	if y > v.keyCY && y < v.keyCY+v.keyLen {
		t := (y - v.keyCY) / v.keyLen
		half := 0.042 + 0.046*t
		return math.Abs(dx) <= half
	}
	return false
}
