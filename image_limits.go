package main

import (
	"fmt"
	"image"
)

const (
	defaultMaxPixels int64 = 64_000_000
	jpegMaxPixels    int64 = 110_000_000
)

type imagePixelLimitError struct {
	width, height int
	limit         int64
}

func (e *imagePixelLimitError) Error() string {
	return fmt.Sprintf("image too many pixels: %d×%d, limit %d", e.width, e.height, e.limit)
}

func (e *imagePixelLimitError) message() string {
	return fmt.Sprintf("图片有 %d 像素，超过 %d 像素上限", int64(e.width)*int64(e.height), e.limit)
}

// The boolean reports whether processing needs all generation slots.
func imageBudget(info image.Config, format string, normalLimit int64) (bool, error) {
	if normalLimit <= 0 {
		normalLimit = defaultMaxPixels
	}
	if info.Width <= 0 || info.Height <= 0 {
		return false, fmt.Errorf("invalid image dimensions")
	}
	limit := normalLimit
	// Custom limits used by callers and tests also constrain JPEG.
	if format == "jpeg" && normalLimit == defaultMaxPixels {
		limit = jpegMaxPixels
	}
	if int64(info.Width) > limit/int64(info.Height) {
		return false, &imagePixelLimitError{info.Width, info.Height, limit}
	}
	return int64(info.Width) > normalLimit/int64(info.Height), nil
}
