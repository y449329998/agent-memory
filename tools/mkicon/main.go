// mkicon 生成应用图标：深色圆角方块 + 两个带缺口的环形箭头。
// 只依赖标准库；先按 4 倍尺寸绘制再盒式降采样，得到平滑边缘。
// 输出 internal/desktop/assets/app.png 与 app.ico（单条目 PNG ICO）。
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

const (
	finalSize  = 256
	scale      = 4
	drawSize   = finalSize * scale
	cornerR    = 56.0 * scale
	ringOuter  = 78.0 * scale
	ringInner  = 62.0 * scale
	ringOuter2 = 52.0 * scale
	ringInner2 = 36.0 * scale
	strokeGap  = math.Pi / 2
)

var (
	colBg     = color.NRGBA{24, 24, 27, 255}
	colRing   = color.NRGBA{129, 140, 248, 255}
	colAccent = color.NRGBA{79, 70, 229, 255}
	colClear  = color.NRGBA{0, 0, 0, 0}
)

func main() {
	out := "internal/desktop/assets"
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	big := render(drawSize)
	img := downsample(big, scale)

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "app.png"), pngBuf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "app.ico"), wrapICO(pngBuf.Bytes()), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s/app.png and app.ico", out)
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size) / 256
	cx, cy := float64(size)/2, float64(size)/2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if !insideRounded(fx, fy, float64(size), 48*s) {
				img.SetNRGBA(x, y, colClear)
				continue
			}
			img.SetNRGBA(x, y, colBg)
			dx, dy := fx-cx, fy-cy
			d := math.Hypot(dx, dy)
			ang := math.Atan2(dy, dx)
			if d >= 73*s && d <= 91*s && !inGap(ang, -math.Pi/4, 70) {
				img.SetNRGBA(x, y, colRing)
			}
			if d >= 40*s && d <= 56*s && !inGap(ang, math.Pi*0.75, 70) {
				img.SetNRGBA(x, y, colAccent)
			}
		}
	}
	return img
}

// insideRounded 判断点是否在圆角矩形内（整张画布就是圆角矩形）。
func insideRounded(x, y, size, r float64) bool {
	c := size - r
	switch {
	case x < r && y < r:
		return math.Hypot(r-x, r-y) <= r
	case x > c && y < r:
		return math.Hypot(x-c, r-y) <= r
	case x < r && y > c:
		return math.Hypot(r-x, y-c) <= r
	case x > c && y > c:
		return math.Hypot(x-c, y-c) <= r
	}
	return true
}

// inArc 让环留出一个 90° 缺口，两个环的缺口相对。
// inGap 判断角度是否落在以 center 为中心、宽 widthDeg 度的缺口内。
func inGap(ang, center float64, widthDeg float64) bool {
	half := widthDeg * math.Pi / 360
	diff := math.Mod(ang-center+3*math.Pi, 2*math.Pi) - math.Pi
	return math.Abs(diff) < half
}

func downsample(src *image.NRGBA, factor int) *image.NRGBA {
	w := src.Bounds().Dx() / factor
	h := src.Bounds().Dy() / factor
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b, a, n uint32
			for sy := 0; sy < factor; sy++ {
				for sx := 0; sx < factor; sx++ {
					c := src.NRGBAAt(x*factor+sx, y*factor+sy)
					r += uint32(c.R) * uint32(c.A)
					g += uint32(c.G) * uint32(c.A)
					b += uint32(c.B) * uint32(c.A)
					a += uint32(c.A)
					n++
				}
			}
			if a == 0 {
				dst.SetNRGBA(x, y, colClear)
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / n),
			})
		}
	}
	return dst
}

// wrapICO 组装单条目 ICO（PNG 负载，Windows Vista+ 支持）。
func wrapICO(pngData []byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	buf.WriteByte(0) // width 256
	buf.WriteByte(0) // height 256
	buf.WriteByte(0) // palette
	buf.WriteByte(0) // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(32))
	binary.Write(&buf, binary.LittleEndian, uint32(len(pngData)))
	binary.Write(&buf, binary.LittleEndian, uint32(6+16))
	buf.Write(pngData)
	return buf.Bytes()
}
