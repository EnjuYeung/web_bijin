//go:build ignore

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	root := "photos"
	must(os.MkdirAll(filepath.Join(root, "子目录"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "misc"), 0o755))
	must(os.MkdirAll(filepath.Join(root, ".hidden"), 0o755))

	writeJPEG(filepath.Join(root, "wide.jpg"), 1600, 900, color.RGBA{196, 92, 48, 255})
	writeJPEG(filepath.Join(root, "tall.jpg"), 700, 1400, color.RGBA{48, 92, 160, 255})
	writeJPEG(filepath.Join(root, "square.jpg"), 800, 800, color.RGBA{48, 140, 110, 255})
	writeJPEG(filepath.Join(root, "子目录", "海边 日落.jpg"), 1200, 800, color.RGBA{210, 130, 60, 255})
	writeJPEG(filepath.Join(root, ".hidden", "secret.jpg"), 200, 200, color.RGBA{10, 10, 10, 255})
	writeJPEG(filepath.Join(root, "big.jpg"), 4000, 2800, color.RGBA{90, 70, 120, 255})
	must(os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not an image"), 0o644))
	must(os.WriteFile(filepath.Join(root, "clip.mp4"), []byte("fake video"), 0o644))
	must(os.WriteFile(filepath.Join(root, "broken.jpg"), []byte("this is not a jpeg"), 0o644))
	must(os.WriteFile(filepath.Join(root, "noext"), []byte("xx"), 0o644))

	pf, err := os.Create(filepath.Join(root, "misc", "tile.png"))
	must(err)
	must(png.Encode(pf, solid(240, 240, color.RGBA{220, 200, 160, 255})))
	pf.Close()

	gf, err := os.Create(filepath.Join(root, "misc", "dot.gif"))
	must(err)
	must(gif.Encode(gf, solid(80, 160, color.RGBA{30, 160, 90, 255}), nil))
	gf.Close()

	// 1x1 lossy webp
	webp := []byte{
		0x52, 0x49, 0x46, 0x46, 0x24, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50,
		0x56, 0x50, 0x38, 0x20, 0x18, 0x00, 0x00, 0x00, 0x30, 0x01, 0x00, 0x9d,
		0x01, 0x2a, 0x01, 0x00, 0x01, 0x00, 0x02, 0x00, 0x34, 0x25, 0xa4, 0x00,
		0x03, 0x70, 0x00, 0xfe, 0xfb, 0x94, 0x00, 0x00,
	}
	must(os.WriteFile(filepath.Join(root, "misc", "dot.webp"), webp, 0o644))

	for i := 1; i <= 50; i++ {
		c := color.RGBA{uint8(40 + i*3), uint8(80 + i), uint8(70 + i*2), 255}
		name := filepath.Join(root, "batch", fmt.Sprintf("%02d.jpg", i))
		h := 300 + (i%5)*40
		w := 400 + (i%3)*30
		writeJPEG(name, w, h, c)
	}
}

func writeJPEG(path string, w, h int, c color.RGBA) {
	must(os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	must(jpeg.Encode(f, solid(w, h, c), &jpeg.Options{Quality: 80}))
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = 255
	}
	return img
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
