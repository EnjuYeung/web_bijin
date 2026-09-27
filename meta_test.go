package main

import "testing"

func TestPhotoTitleAndFormat(t *testing.T) {
	if photoTitle("子目录/海边 日落.jpg") != "海边 日落.jpg" {
		t.Fatal(photoTitle("子目录/海边 日落.jpg"))
	}
	if photoFormat("a.JPG") != "JPEG" || photoFormat("x.webp") != "WEBP" || photoFormat("t.png") != "PNG" {
		t.Fatal("format")
	}
}
