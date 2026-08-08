//go:build ignore

// Рисует варианты иконки приложения: щит в цвет логотипа с замком или
// замочной скважиной внутри, фон прозрачный.
//
//	go run tools/mkshield/main.go <папка-результата>
//
// Каждый вариант сохраняется отдельным PNG в 512 и в 32 пикселя — второй
// нужен, чтобы сразу видеть, не разваливается ли рисунок в размере значка
// в трее. Выбранный вариант потом превращается в .ico тем же mkicon.
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
var (
	cyan = color.RGBA{0x2d, 0xe2, 0xff, 0xff}
	deep = color.RGBA{0x12, 0x8c, 0xd8, 0xff} // нижняя точка градиента
)

type variant struct {
	name string
	desc string
	// filled сообщает, закрашена ли точка (x,y) в координатах 0..1.
	filled func(x, y float64) bool
	grad   bool
}

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
			img := render(v, size)
			name := fmt.Sprintf("%s-%d.png", v.name, size)
			f, err := os.Create(filepath.Join(out, name))
			if err != nil {
				panic(err)
			}
			if err := png.Encode(f, img); err != nil {
				panic(err)
			}
			f.Close()
		}
		fmt.Printf("%-12s %s\n", v.name, v.desc)
	}
}

func variants() []variant {
	return []variant{
		{
			name: "1-skvazhina",
			desc: "сплошной щит, замочная скважина вырезана насквозь",
			filled: func(x, y float64) bool {
				return inShield(x, y) && !inKeyhole(x, y)
			},
		},
		{
			name: "2-zamok",
			desc: "сплошной щит, навесной замок вырезан насквозь",
			filled: func(x, y float64) bool {
				return inShield(x, y) && !inPadlock(x, y)
			},
		},
		{
			name: "3-kontur-skvazhina",
			desc: "контур щита, скважина внутри",
			filled: func(x, y float64) bool {
				return shieldOutline(x, y, 0.085) || inKeyhole(x, y)
			},
		},
		{
			name: "4-kontur-zamok",
			desc: "контур щита, замок внутри",
			filled: func(x, y float64) bool {
				return shieldOutline(x, y, 0.085) || inPadlock(x, y)
			},
		},
		{
			name: "5-gradient",
			desc: "сплошной щит с градиентом, скважина вырезана",
			grad: true,
			filled: func(x, y float64) bool {
				return inShield(x, y) && !inKeyhole(x, y)
			},
		},
		{
			name: "6-dvoynoy",
			desc: "щит с внутренней окантовкой, скважина вырезана",
			filled: func(x, y float64) bool {
				if !inShield(x, y) {
					return false
				}
				// Тонкая прорезь по контуру внутри — щит выглядит объёмнее.
				if shieldOutline(x, y, 0.055) {
					return true
				}
				gap := scaleAboutCenter(x, y, 1.0/0.80)
				if inShield(gap.x, gap.y) {
					return !inKeyhole(x, y)
				}
				return false
			},
		},
	}
}

// render рисует вариант со сглаживанием: покрытие пикселя считается по сетке
// 4x4. Без этого тонкие места (скважина, окантовка) рвутся уже на 64 пикселях.
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
					if v.filled(x, y) {
						hits++
					}
				}
			}
			if hits == 0 {
				continue
			}
			a := float64(hits) / float64(ss*ss)

			c := cyan
			if v.grad {
				t := float64(py) / s
				c = color.RGBA{
					R: uint8(float64(cyan.R)*(1-t) + float64(deep.R)*t),
					G: uint8(float64(cyan.G)*(1-t) + float64(deep.G)*t),
					B: uint8(float64(cyan.B)*(1-t) + float64(deep.B)*t),
					A: 255,
				}
			}
			img.Set(px, py, color.NRGBA{c.R, c.G, c.B, uint8(a*255 + 0.5)})
		}
	}
	return img
}

// ---------- формы (координаты 0..1) ----------

// inShield — щит: плечи со скруглёнными верхними углами и сходящееся книзу
// остриё. Доля прямой части намеренно небольшая (до 42% высоты): если сделать
// её больше, силуэт перестаёт читаться как щит и выглядит скруглённым
// квадратом.
func inShield(x, y float64) bool {
	const padX, top, bottom = 0.115, 0.06, 0.965
	if y < top || y > bottom {
		return false
	}
	w := 1 - 2*padX
	nx := (x - padX) / w
	ny := (y - top) / (bottom - top)
	if nx < 0 || nx > 1 {
		return false
	}
	dx := math.Abs(nx-0.5) * 2

	const straight = 0.42
	if ny < straight {
		r := 0.17 // скругление верхних углов
		if ny < r && dx > 1-r*2 {
			ex := (dx - (1 - r*2)) / (r * 2)
			ey := (r - ny) / r
			return ex*ex+ey*ey <= 1
		}
		return true
	}
	// Остриё: четверть эллипса даёт плавные бока и аккуратный кончик.
	k := (ny - straight) / (1 - straight)
	return dx <= math.Sqrt(math.Max(0, 1-k*k))
}

// shieldOutline — кольцо вдоль края щита заданной толщины.
func shieldOutline(x, y, thick float64) bool {
	if !inShield(x, y) {
		return false
	}
	inner := scaleAboutCenter(x, y, 1/(1-thick*2))
	return !inShield(inner.x, inner.y)
}

type pt struct{ x, y float64 }

// scaleAboutCenter растягивает точку относительно визуального центра щита —
// так получается равномерный отступ внутрь без сложной математики контуров.
func scaleAboutCenter(x, y, k float64) pt {
	const cx, cy = 0.5, 0.44
	return pt{cx + (x-cx)*k, cy + (y-cy)*k}
}

// inKeyhole — замочная скважина: круг и сужающаяся книзу прорезь.
func inKeyhole(x, y float64) bool {
	const cx, cy = 0.5, 0.38
	dx, dy := x-cx, y-cy

	if math.Hypot(dx, dy) <= 0.105 {
		return true
	}
	// Прорезь: сверху уже, книзу шире — так читается даже в 16 пикселей.
	if y > cy && y < cy+0.23 {
		t := (y - cy) / 0.23
		half := 0.045 + 0.045*t
		return math.Abs(dx) <= half
	}
	return false
}

// inPadlock — навесной замок: корпус со скруглёнными углами и дужка сверху.
func inPadlock(x, y float64) bool {
	const cx = 0.5

	// Корпус.
	const bt, bb, bhw = 0.40, 0.66, 0.150
	if y >= bt && y <= bb && math.Abs(x-cx) <= bhw {
		r := 0.045
		dx := math.Abs(x-cx) - (bhw - r)
		var dy float64
		if y < bt+r {
			dy = bt + r - y
		} else if y > bb-r {
			dy = y - (bb - r)
		}
		if dx > 0 && dy > 0 {
			return dx*dx+dy*dy <= r*r
		}
		return true
	}

	// Дужка: полукольцо, упирающееся в корпус.
	const sc, sr, st = 0.40, 0.095, 0.038
	if y <= sc {
		d := math.Hypot(x-cx, y-sc)
		return math.Abs(d-sr) <= st/2+0.012
	}
	return false
}
