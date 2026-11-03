package main

import (
	"errors"
	"image"
	"testing"
)

func TestImageBudget(t *testing.T) {
	for _, tc := range []struct {
		format    string
		width     int
		height    int
		exclusive bool
		rejected  bool
	}{
		{"jpeg", 7999, 8000, false, false},
		{"jpeg", 8000, 8000, false, false},
		{"jpeg", 8001, 8000, true, false},
		{"jpeg", 10999, 10000, true, false},
		{"jpeg", 11000, 10000, true, false},
		{"jpeg", 11001, 10000, false, true},
		{"png", 8000, 8000, false, false},
		{"png", 8001, 8000, false, true},
		{"jpeg", 0, 10000, false, true},
	} {
		exclusive, err := imageBudget(image.Config{Width: tc.width, Height: tc.height}, tc.format, defaultMaxPixels)
		if exclusive != tc.exclusive || (err != nil) != tc.rejected {
			t.Fatalf("%+v: exclusive=%v err=%v", tc, exclusive, err)
		}
	}
	_, err := imageBudget(image.Config{Width: 11001, Height: 10000}, "jpeg", defaultMaxPixels)
	var limitErr *imagePixelLimitError
	if !errors.As(err, &limitErr) || limitErr.message() != "图片有 110010000 像素，超过 110000000 像素上限" {
		t.Fatalf("wrong pixel rejection: %v", err)
	}
}
