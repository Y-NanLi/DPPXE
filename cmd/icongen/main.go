// icongen：把一张图片（webp/png）转成多尺寸 Windows .ico。
//
// 用法：
//
//	go run ./cmd/icongen -src ../../icon-src.webp -out ../../app.ico
//
// 生成的 ico 用 PNG 压缩帧（Vista+ 支持），尺寸覆盖 16/24/32/48/64/128/256。
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

func main() {
	src := flag.String("src", "", "源图片路径（.webp / .png）")
	out := flag.String("out", "app.ico", "输出 .ico 路径")
	pngOut := flag.String("png", "", "额外输出一张 256px PNG（给窗口图标用，可为空）")
	flag.Parse()
	if *src == "" {
		log.Fatal("必须指定 -src")
	}

	img, err := load(*src)
	if err != nil {
		log.Fatalf("读取图片失败: %v", err)
	}
	b := img.Bounds()
	log.Printf("源图 %dx%d", b.Dx(), b.Dy())

	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	frames := make([][]byte, len(sizes))
	for i, s := range sizes {
		dst := image.NewRGBA(image.Rect(0, 0, s, s))
		// CatmullRom 缩放质量好，缩到 16px 也不会糊成一团
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			log.Fatalf("编码 %dpx 失败: %v", s, err)
		}
		frames[i] = buf.Bytes()
	}

	var head bytes.Buffer
	_ = binary.Write(&head, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&head, binary.LittleEndian, uint16(1)) // type = icon
	_ = binary.Write(&head, binary.LittleEndian, uint16(len(sizes)))

	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0 // ICO 里 256 用 0 表示
		}
		head.WriteByte(w) // width
		head.WriteByte(w) // height
		head.WriteByte(0) // color count
		head.WriteByte(0) // reserved
		_ = binary.Write(&head, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&head, binary.LittleEndian, uint16(32)) // bit count
		_ = binary.Write(&head, binary.LittleEndian, uint32(len(frames[i])))
		_ = binary.Write(&head, binary.LittleEndian, uint32(offset))
		offset += len(frames[i])
	}

	outData := append(head.Bytes(), bytes.Join(frames, nil)...)
	if err := os.WriteFile(*out, outData, 0o644); err != nil {
		log.Fatalf("写 ico 失败: %v", err)
	}
	log.Printf("已生成 %s（%d 字节，%d 个尺寸）", *out, len(outData), len(sizes))

	if *pngOut != "" {
		dst := image.NewRGBA(image.Rect(0, 0, 256, 256))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		var buf bytes.Buffer
		if err := png.Encode(&buf, dst); err != nil {
			log.Fatalf("编码 PNG 失败: %v", err)
		}
		if err := os.WriteFile(*pngOut, buf.Bytes(), 0o644); err != nil {
			log.Fatalf("写 PNG 失败: %v", err)
		}
		log.Printf("已生成 %s（%d 字节）", *pngOut, buf.Len())
	}
}

func load(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if strings.EqualFold(filepath.Ext(path), ".webp") {
		return webp.Decode(f)
	}
	img, _, err := image.Decode(f)
	return img, err
}
