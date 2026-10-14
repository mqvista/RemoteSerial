// mkicon 生成托盘和窗口用的图标文件（ICO 容器内嵌 PNG），避免在仓库里放二进制来源不明的图片。
//
//	go run ./tools/mkicon -o internal/shell/icon.ico
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	out := flag.String("o", "icon.ico", "输出文件")
	flag.Parse()

	sizes := []int{16, 32, 48, 256}
	var pngs [][]byte
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, draw(s)); err != nil {
			panic(err)
		}
		pngs = append(pngs, buf.Bytes())
	}

	var ico bytes.Buffer
	// ICONDIR
	binary.Write(&ico, binary.LittleEndian, uint16(0))
	binary.Write(&ico, binary.LittleEndian, uint16(1))
	binary.Write(&ico, binary.LittleEndian, uint16(len(sizes)))
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0
		}
		ico.WriteByte(w)
		ico.WriteByte(w)
		ico.WriteByte(0)
		ico.WriteByte(0)
		binary.Write(&ico, binary.LittleEndian, uint16(1))
		binary.Write(&ico, binary.LittleEndian, uint16(32))
		binary.Write(&ico, binary.LittleEndian, uint32(len(pngs[i])))
		binary.Write(&ico, binary.LittleEndian, uint32(offset))
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		ico.Write(p)
	}
	if err := os.WriteFile(*out, ico.Bytes(), 0o644); err != nil {
		panic(err)
	}
}

// draw 画一个深蓝圆角方块，中间是白色的串口针脚图案：上排 5 针、下排 4 针。
func draw(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bg := color.NRGBA{R: 0x1f, G: 0x4e, B: 0x8c, A: 0xff}
	fg := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	f := float64(size)
	radius := f * 0.22
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if inRoundedRect(float64(x)+0.5, float64(y)+0.5, f, radius) {
				img.SetNRGBA(x, y, bg)
			}
		}
	}
	// 针脚：圆点
	dot := f * 0.07
	if dot < 1 {
		dot = 1
	}
	rows := [][]float64{
		{0.22, 0.36, 0.50, 0.64, 0.78},
		{0.29, 0.43, 0.57, 0.71},
	}
	ys := []float64{0.40, 0.60}
	for r, xs := range rows {
		for _, px := range xs {
			fillCircle(img, px*f, ys[r]*f, dot, fg)
		}
	}
	// 外框：梯形轮廓的近似，用一条细线框住
	thick := f * 0.045
	if thick < 1 {
		thick = 1
	}
	strokeRoundedRect(img, f*0.12, f*0.28, f*0.88, f*0.72, thick, fg)
	return img
}

func inRoundedRect(x, y, size, r float64) bool {
	if x < 0 || y < 0 || x > size || y > size {
		return false
	}
	cx := clamp(x, r, size-r)
	cy := clamp(y, r, size-r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func strokeRoundedRect(img *image.NRGBA, x0, y0, x1, y1, thick float64, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			inOuter := px >= x0 && px <= x1 && py >= y0 && py <= y1
			inInner := px >= x0+thick && px <= x1-thick && py >= y0+thick && py <= y1-thick
			if inOuter && !inInner {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}
