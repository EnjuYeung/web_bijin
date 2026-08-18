package main

import (
	"io"
	"os"
)

// exifSwap reports whether EXIF orientation transposes width and height.
func exifSwap(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	o, err := readOrientation(f)
	if err != nil {
		return false
	}
	return o >= 5 && o <= 8
}

func readOrientation(r io.Reader) (int, error) {
	// imaging.Open already honors orientation for thumbs.
	// For DecodeConfig we only need the orientation tag; imaging
	// does not export a helper, so a tiny EXIF walk lives here.
	return peekOrientation(r)
}

func peekOrientation(r io.Reader) (int, error) {
	const (
		soi  = 0xFFD8
		app1 = 0xFFE1
	)
	b := make([]byte, 2)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, err
	}
	if int(b[0])<<8|int(b[1]) != soi {
		return 1, nil
	}
	for {
		if _, err := io.ReadFull(r, b); err != nil {
			return 1, nil
		}
		for b[0] != 0xFF {
			b[0] = b[1]
			if _, err := io.ReadFull(r, b[1:]); err != nil {
				return 1, nil
			}
		}
		marker := int(b[0])<<8 | int(b[1])
		for marker == 0xFFFF {
			if _, err := io.ReadFull(r, b[1:]); err != nil {
				return 1, nil
			}
			marker = int(b[0])<<8 | int(b[1])
		}
		if marker == 0xFFDA || marker == 0xFFD9 {
			return 1, nil
		}
		if _, err := io.ReadFull(r, b); err != nil {
			return 1, nil
		}
		n := int(b[0])<<8 | int(b[1])
		if n < 2 {
			return 1, nil
		}
		payload := make([]byte, n-2)
		if _, err := io.ReadFull(r, payload); err != nil {
			return 1, nil
		}
		if marker != app1 {
			continue
		}
		if o, ok := orientationFromAPP1(payload); ok {
			return o, nil
		}
	}
}

func orientationFromAPP1(p []byte) (int, bool) {
	if len(p) < 12 || string(p[:6]) != "Exif\x00\x00" {
		return 0, false
	}
	tiff := p[6:]
	var le bool
	switch string(tiff[:2]) {
	case "II":
		le = true
	case "MM":
		le = false
	default:
		return 0, false
	}
	if u16(tiff[2:4], le) != 42 || len(tiff) < 8 {
		return 0, false
	}
	off := int(u32(tiff[4:8], le))
	return walkIFDOrientation(tiff, off, le)
}

func walkIFDOrientation(tiff []byte, off int, le bool) (int, bool) {
	if off < 0 || off+2 > len(tiff) {
		return 0, false
	}
	n := int(u16(tiff[off:off+2], le))
	off += 2
	for i := 0; i < n; i++ {
		if off+12 > len(tiff) {
			return 0, false
		}
		tag := u16(tiff[off:off+2], le)
		typ := u16(tiff[off+2:off+4], le)
		if tag == 0x0112 && typ == 3 {
			return int(u16(tiff[off+8:off+10], le)), true
		}
		off += 12
	}
	return 0, false
}

func u16(b []byte, le bool) uint16 {
	if le {
		return uint16(b[0]) | uint16(b[1])<<8
	}
	return uint16(b[1]) | uint16(b[0])<<8
}

func u32(b []byte, le bool) uint32 {
	if le {
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	}
	return uint32(b[3]) | uint32(b[2])<<8 | uint32(b[1])<<16 | uint32(b[0])<<24
}
