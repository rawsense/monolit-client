package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Знак Monolit — строчная m из двух арок-проёмов (вариант 37д) на тёмной плашке. Включено: арки цвета бренда,
// выключено: серые. Плашка одна и та же — иконка узнаётся и в трее, и на панели задач
var (
	colTile = color.NRGBA{0x1c, 0x21, 0x28, 0xff}
	colOn   = color.NRGBA{0x7f, 0xa3, 0xc7, 0xff}
	colOff  = color.NRGBA{0x8a, 0x8f, 0x98, 0xff}
)

// arch — арка в сетке 24×24: прямоугольник от yc до низа и полуэллипс сверху
type arch struct{ cx, rx, yc, ry, bottom float64 }

func (a arch) in(x, y float64) bool {
	if y > a.bottom || x < a.cx-a.rx || x > a.cx+a.rx {
		return false
	}
	if y >= a.yc {
		return true
	}
	dx, dy := (x-a.cx)/a.rx, (y-a.yc)/a.ry
	return dx*dx+dy*dy <= 1
}

// форма знака: две внешние арки с общей средней опорой, в каждой вырезан проём. Всё поднято на 0,4,
// чтобы знак стоял по центру плашки (низ ровный, верх скруглён)
var (
	markOuter = []arch{{8.85, 4.35, 10.1, 4.7, 18.6}, {15.15, 4.35, 10.1, 4.7, 18.6}}
	markHoles = []arch{{8.85, 1.55, 10.6, 1.7, 18.7}, {15.15, 1.55, 10.6, 1.7, 18.7}}
)

func inMark(x, y float64) bool {
	if !markOuter[0].in(x, y) && !markOuter[1].in(x, y) {
		return false
	}
	return !markHoles[0].in(x, y) && !markHoles[1].in(x, y)
}

// плашка: квадрат 1..23 со скруглением 6
func inTile(x, y float64) bool {
	const x0, y0, x1, y1, r = 1, 1, 23, 23, 6
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	dx := math.Max(math.Max(x0+r-x, x-(x1-r)), 0)
	dy := math.Max(math.Max(y0+r-y, y-(y1-r)), 0)
	return dx*dx+dy*dy <= r*r
}

// markPNG рисует иконку: 8×8 замеров на пиксель — края гладкие и на 16 px, и на 256
func markPNG(size int, c color.NRGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const n = 8
	s := 24 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			tile, mark := 0, 0
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					x := (float64(px) + (float64(sx)+.5)/n) * s
					y := (float64(py) + (float64(sy)+.5)/n) * s
					switch {
					case !inTile(x, y):
					case inMark(x, y):
						mark++
					default:
						tile++
					}
				}
			}
			cov := tile + mark
			if cov == 0 {
				continue
			}
			mix := func(t, m uint8) uint8 { return uint8((int(t)*tile + int(m)*mark + cov/2) / cov) }
			img.SetNRGBA(px, py, color.NRGBA{mix(colTile.R, c.R), mix(colTile.G, c.G), mix(colTile.B, c.B), uint8(cov * 255 / (n * n))})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// icoBytes: ICO с PNG внутри (Windows Vista+), размеры 16, 32, 48, 256
func icoBytes(c color.NRGBA) []byte {
	sizes := []int{16, 32, 48, 256}
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, markPNG(s, c))
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0
		}
		b.Write([]byte{w, w, 0, 0})
		binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, im := range imgs {
		b.Write(im)
	}
	return b.Bytes()
}
