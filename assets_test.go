package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

func TestFrontendCompression(t *testing.T) {
	srv, _, _ := testApp(t)
	var path string
	err := fs.WalkDir(webEmbed, "web/_next/static", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "" && !entry.IsDir() && strings.HasSuffix(name, ".js") {
			path = strings.TrimPrefix(name, "web")
		}
		return nil
	})
	if err != nil || path == "" {
		t.Fatalf("build the frontend first: %v", err)
	}
	expected, err := fs.ReadFile(webEmbed, "web"+path)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	for _, accept := range []string{"gzip, deflate, br", "gzip;q=0, identity", "identity"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Accept-Encoding", accept)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "javascript") || res.Header.Get("Vary") != "Accept-Encoding" {
			t.Fatalf("asset response %s: %d %v", accept, res.StatusCode, res.Header)
		}
		if accept == "gzip, deflate, br" {
			if res.Header.Get("Content-Encoding") != "gzip" || len(body) >= len(expected) {
				t.Fatal("expected a smaller gzip response")
			}
			reader, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else if res.Header.Get("Content-Encoding") != "" {
			t.Fatal("gzip must respect explicit refusal")
		}
		if !bytes.Equal(body, expected) {
			t.Fatalf("asset body changed for %s", accept)
		}
	}
}

func TestExportedFrontendAssets(t *testing.T) {
	srv, _, _ := testApp(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var assets []string
	err := fs.WalkDir(webEmbed, "web/_next/static", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && (strings.HasSuffix(name, ".css") || strings.HasSuffix(name, ".js")) {
			assets = append(assets, strings.TrimPrefix(name, "web"))
		}
		return nil
	})
	if err != nil || len(assets) == 0 {
		t.Fatalf("build the frontend first: assets=%d err=%v", len(assets), err)
	}
	for _, path := range assets {
		res, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("static asset %s: status=%d cache=%s", path, res.StatusCode, res.Header.Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/_next/static/", "/_next/static/chunks/", "/_next/static/missing.js", "/index.html", "/index.txt", "/login.html", "/login/index.html", "/_next/static/../../index.html"} {
		res, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Fatalf("directory or exported page should not be public: %s", path)
		}
	}
}
