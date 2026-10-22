package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type imageReadProbe struct {
	reader  io.Reader
	maxRead int
}

func (r *imageReadProbe) Read(p []byte) (int, error) {
	r.maxRead = max(r.maxRead, len(p))
	return r.reader.Read(p)
}

// CVE-2026-33809: a tiny TIFF must not request a buffer proportional to a
// forged IFD offset. Use image.DecodeConfig, as the app does, so the input
// passes through the registry's bufio.Reader rather than a direct ReaderAt.
func TestTIFFHeaderReadIsBounded(t *testing.T) {
	for _, endian := range []struct {
		name  string
		magic string
		order binary.ByteOrder
	}{
		{"little-endian", "II", binary.LittleEndian},
		{"big-endian", "MM", binary.BigEndian},
	} {
		t.Run(endian.name, func(t *testing.T) {
			header := make([]byte, 8)
			copy(header, endian.magic)
			endian.order.PutUint16(header[2:], 42)
			endian.order.PutUint32(header[4:], 32<<20)
			r := &imageReadProbe{reader: bytes.NewReader(header)}
			_, format, err := image.DecodeConfig(r)
			if format != "tiff" || err == nil {
				t.Fatalf("malformed TIFF: format=%q err=%v", format, err)
			}
			if r.maxRead > 16<<20 {
				t.Fatalf("8-byte header requested a %d-byte read for forged offset", r.maxRead)
			}
		})
	}
}

// A 1x1, 8-bit BMP declares one palette entry but uses index 255. Before the
// CVE-2026-42500 fix it passed DecodeConfig and Decode, then panicked during
// JPEG encoding. Give it a .jpg source key to cover content-based dispatch.
func invalidPaletteBMP() []byte {
	data := make([]byte, 62)
	copy(data, "BM")
	binary.LittleEndian.PutUint32(data[2:], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[10:], 58)
	binary.LittleEndian.PutUint32(data[14:], 40)
	binary.LittleEndian.PutUint32(data[18:], 1)
	binary.LittleEndian.PutUint32(data[22:], 1)
	binary.LittleEndian.PutUint16(data[26:], 1)
	binary.LittleEndian.PutUint16(data[28:], 8)
	binary.LittleEndian.PutUint32(data[34:], 4)
	binary.LittleEndian.PutUint32(data[46:], 1)
	data[58] = 255
	return data
}

func TestMalformedPalettePhotoIsInvalidAndCanBeReplaced(t *testing.T) {
	st, src, thumbs, sc := fixSetup(t)
	src.data = invalidPaletteBMP()
	src.object.Size = int64(len(src.data))
	result := sc.photos.SyncKey(context.Background(), "a.jpg")
	var invalid *invalidImageError
	if !result.Invalid || !errors.As(result.Err, &invalid) || result.AlbumUsable || result.Retryable {
		t.Fatalf("malformed photo was not rejected: %+v", result)
	}
	p, ok, err := st.getBySourceKey("a.jpg")
	if err != nil || !ok || !p.Broken || thumbs.exists(p) {
		t.Fatalf("malformed photo published: %+v ok=%v err=%v", p, ok, err)
	}
	reads := src.opens.Load()
	if again := sc.photos.SyncKey(context.Background(), "a.jpg"); !again.Invalid || again.Err != nil || src.opens.Load() != reads {
		t.Fatalf("unchanged bad version was retried: %+v", again)
	}
	src.data = fixtureJPEG(color.RGBA{80, 40, 120, 255})
	src.object.Size, src.object.Version = int64(len(src.data)), "v2"
	result = sc.photos.SyncKey(context.Background(), "a.jpg")
	if result.Err != nil || result.Invalid || !result.AlbumUsable || !result.WallpapersReady {
		t.Fatalf("valid replacement failed: %+v", result)
	}
	p, ok, err = st.getBySourceKey("a.jpg")
	if err != nil || !ok || p.Broken || !thumbs.exists(p) {
		t.Fatalf("valid replacement not published: %+v ok=%v err=%v", p, ok, err)
	}
}

// The upload validator parses image contents before comparing the MIME type.
// Malformed decoder inputs must be rejected without saving a source file.
func TestMalformedImageUploadIsRejected(t *testing.T) {
	for _, endian := range []struct {
		name  string
		order binary.ByteOrder
		magic string
	}{
		{"little-endian-tiff", binary.LittleEndian, "II"},
		{"big-endian-tiff", binary.BigEndian, "MM"},
		{"invalid-palette-bmp", nil, ""},
	} {
		t.Run(endian.name, func(t *testing.T) {
			data := invalidPaletteBMP()
			if endian.order != nil {
				data = make([]byte, 8)
				copy(data, endian.magic)
				endian.order.PutUint16(data[2:], 42)
				endian.order.PutUint32(data[4:], 32<<20)
			}
			a := newUploadTestApp(t, nil)
			prepared := a.prepare(t, "local", "invalid.jpg", len(data))
			response, body := a.request(t, http.MethodPut, prepared.URL, data)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("malformed upload status=%d body=%s", response.StatusCode, body)
			}
			code, state, task := verifyUploadTest(t, a, prepared.Task.ID)
			if code != http.StatusOK || state != "rejected" || task.Saved || task.TransferRetryable {
				t.Fatalf("malformed upload verification: %d %s %+v", code, state, task)
			}
			if _, err := os.Stat(filepath.Join(a.cfg.PhotosDir, "invalid.jpg")); !os.IsNotExist(err) {
				t.Fatalf("malformed original was published: %v", err)
			}
		})
	}
}
