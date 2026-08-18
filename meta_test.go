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

func TestHumanBytes(t *testing.T) {
	if humanBytes(800) != "800 B" {
		t.Fatal(humanBytes(800))
	}
	if humanBytes(2048) != "2 KB" {
		t.Fatal(humanBytes(2048))
	}
	if humanBytes(2_400_000) != "2.3 MB" {
		t.Fatal(humanBytes(2_400_000))
	}
}
