//go:build ignore

// icongen 把任意图片（PNG/JPEG）转换成多尺寸 .ico 图标文件。
//
// Windows 的 .ico 从 Vista 起支持 PNG 压缩条目，这里为每个目标尺寸
// 生成一张缩放的 PNG，再按 ICO 容器格式组装。零第三方依赖。
//
// 用法（在仓库根目录）：
//
//	go run scripts/icongen.go <输入图片> <输出.ico> [尺寸...]
//
// 不指定尺寸时默认：16 24 32 48 64 128 256。图片会先居中裁成正方形再缩放。
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "用法: go run scripts/icongen.go <输入图片> <输出.ico> [尺寸...]")
		os.Exit(2)
	}
	srcPath, outPath := os.Args[1], os.Args[2]

	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	if len(os.Args) > 3 {
		sizes = sizes[:0]
		for _, a := range os.Args[3:] {
			var n int
			if _, err := fmt.Sscanf(a, "%d", &n); err != nil || n <= 0 {
				fmt.Fprintf(os.Stderr, "无效尺寸: %q\n", a)
				os.Exit(2)
			}
			sizes = append(sizes, n)
		}
	}

	f, err := os.Open(srcPath)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		fatal(err)
	}
	src = squareCrop(src)

	// 输出 .png 时只取第一个尺寸，写成普通 PNG（macOS/Linux 托盘用）。
	if len(outPath) >= 4 && outPath[len(outPath)-4:] == ".png" {
		var buf bytes.Buffer
		if err := png.Encode(&buf, resize(src, sizes[0])); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(outPath, buf.Bytes(), 0644); err != nil {
			fatal(err)
		}
		fmt.Printf("已生成 %s（%dpx PNG）\n", outPath, sizes[0])
		return
	}

	// 每个尺寸先编码成 PNG，再写入 ICO 容器。
	entries := make([][]byte, len(sizes))
	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, resize(src, size)); err != nil {
			fatal(err)
		}
		entries[i] = buf.Bytes()
		_ = offset
	}

	var out bytes.Buffer
	// ICONDIR
	writeU16(&out, 0) // reserved
	writeU16(&out, 1) // type: icon
	writeU16(&out, uint16(len(sizes)))

	dataOffset := 6 + 16*len(sizes)
	for i, size := range sizes {
		w, h := size, size
		if w >= 256 {
			w, h = 0, 0 // 0 表示 256
		}
		writeU8(&out, uint8(w))
		writeU8(&out, uint8(h))
		writeU8(&out, 0) // color count
		writeU8(&out, 0) // reserved
		writeU16(&out, 1) // color planes
		writeU16(&out, 32) // bits per pixel
		writeU32(&out, uint32(len(entries[i])))
		writeU32(&out, uint32(dataOffset))
		dataOffset += len(entries[i])
	}
	for _, e := range entries {
		out.Write(e)
	}

	if err := os.WriteFile(outPath, out.Bytes(), 0644); err != nil {
		fatal(err)
	}
	fmt.Printf("已生成 %s（尺寸 %d 个：%v）\n", outPath, len(sizes), sizes)
}

// squareCrop 居中裁成正方形，保证不同长宽比的输入缩放不变形。
func squareCrop(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == h {
		return src
	}
	side := w
	if h < side {
		side = h
	}
	x0, y0 := b.Min.X+(w-side)/2, b.Min.Y+(h-side)/2
	return crop(src, image.Rect(x0, y0, x0+side, y0+side))
}

func crop(src image.Image, r image.Rectangle) image.Image {
	dst := image.NewNRGBA(r.Sub(r.Min))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dst.Set(x-r.Min.X, y-r.Min.Y, src.At(x, y))
		}
	}
	return dst
}

// resize 双线性缩放到目标尺寸。
func resize(src image.Image, size int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	srcB := src.Bounds()
	scaleX := float64(srcB.Dx()) / float64(size)
	scaleY := float64(srcB.Dy()) / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx := (float64(x)+0.5)*scaleX - 0.5
			fy := (float64(y)+0.5)*scaleY - 0.5
			dst.SetNRGBA(x, y, sample(src, fx, fy))
		}
	}
	return dst
}

func sample(src image.Image, fx, fy float64) color.NRGBA {
	b := src.Bounds()
	x0 := int(fx)
	y0 := int(fy)
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	x1 := x0 + 1
	y1 := y0 + 1
	if x1 >= b.Max.X {
		x1 = b.Max.X - 1
	}
	if y1 >= b.Max.Y {
		y1 = b.Max.Y - 1
	}
	tx := fx - float64(x0)
	ty := fy - float64(y0)
	if tx < 0 {
		tx = 0
	}
	if ty < 0 {
		ty = 0
	}
	if tx > 1 {
		tx = 1
	}
	if ty > 1 {
		ty = 1
	}
	c00 := toNRGBA(src.At(x0, y0))
	c10 := toNRGBA(src.At(x1, y0))
	c01 := toNRGBA(src.At(x0, y1))
	c11 := toNRGBA(src.At(x1, y1))
	return lerp(lerp(c00, c10, tx), lerp(c01, c11, tx), ty)
}

func toNRGBA(c color.Color) color.NRGBA {
	return color.NRGBAModel.Convert(c).(color.NRGBA)
}

func lerp(a, b color.NRGBA, t float64) color.NRGBA {
	blend := func(u, v uint8) uint8 {
		return uint8(float64(u) + (float64(v)-float64(u))*t + 0.5)
	}
	return color.NRGBA{R: blend(a.R, b.R), G: blend(a.G, b.G), B: blend(a.B, b.B), A: blend(a.A, b.A)}
}

func writeU8(buf *bytes.Buffer, v uint8)   { buf.WriteByte(v) }
func writeU16(buf *bytes.Buffer, v uint16) { _ = binary.Write(buf, binary.LittleEndian, v) }
func writeU32(buf *bytes.Buffer, v uint32) { _ = binary.Write(buf, binary.LittleEndian, v) }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}

// 确保 jpeg 解码器已注册（logo 可能实际是 JPEG 内容）。
var _ = jpeg.Decode
