package main

import (
	"bytes"
	"encoding/json"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func verifyUploadTest(t *testing.T, a *uploadTestApp, id string) (int, string, uploadTask) {
	t.Helper()
	response, body := a.request(t, http.MethodPost, "/api/uploads/"+id+"/verify", []byte("{}"))
	var result struct {
		State string     `json:"state"`
		Task  uploadTask `json:"task"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result.State, result.Task
}

func TestUploadVerificationRecoversLocalSavedOriginal(t *testing.T) {
	a := newUploadTestApp(t, nil)
	data := uploadTestJPEG(t, color.RGBA{180, 40, 80, 255})
	p := a.prepare(t, "local", "saved.jpg", len(data))
	response, _ := a.request(t, http.MethodPut, p.URL, data)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	// Discard the PUT reply: only the original preparation is known to the caller.
	for range 2 {
		code, state, current := verifyUploadTest(t, a, p.Task.ID)
		if code != 200 || state != "saved" || !current.Saved || current.ID != p.Task.ID {
			t.Fatalf("verification %d %s %+v", code, state, current)
		}
	}
	if a.wait(t, p.Task.ID).Phase != "done" {
		t.Fatal("saved photo did not finish")
	}
	stored, err := os.ReadFile(filepath.Join(a.cfg.PhotosDir, "saved.jpg"))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatal("original changed")
	}
	if n, _ := a.store.countOK(); n != 1 {
		t.Fatalf("indexed %d photos", n)
	}
}

func TestUploadVerificationReadsS3BeyondWaitingTask(t *testing.T) {
	fake := newFakeS3(t, "family")
	a := newUploadTestApp(t, fake)
	data := uploadTestJPEG(t, color.RGBA{40, 110, 180, 255})
	p := a.prepare(t, "s3-1", "cloud.jpg", len(data))
	request, _ := http.NewRequest(http.MethodPut, p.URL, bytes.NewReader(data))
	for name, value := range p.Headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	_, body := a.request(t, http.MethodGet, "/api/uploads/"+p.Task.ID, nil)
	var before struct {
		Task uploadTask `json:"task"`
	}
	json.Unmarshal(body, &before)
	if before.Task.Saved || before.Task.Phase != "waiting" {
		t.Fatal("S3 transmission was already confirmed")
	}
	code, state, current := verifyUploadTest(t, a, p.Task.ID)
	if code != 200 || state != "saved" || !current.Saved {
		t.Fatalf("verification %d %s %+v", code, state, current)
	}
	if a.wait(t, p.Task.ID).Phase != "done" {
		t.Fatal("S3 photo did not finish")
	}
	code, state, _ = verifyUploadTest(t, a, p.Task.ID)
	if code != 200 || state != "saved" {
		t.Fatal("repeat verification lost saved fact")
	}
}

func TestUploadVerificationSeparatesS3AbsenceFromOutage(t *testing.T) {
	fake := newFakeS3(t, "family")
	a := newUploadTestApp(t, fake)
	p := a.prepare(t, "s3-1", "missing.jpg", 10)
	code, state, current := verifyUploadTest(t, a, p.Task.ID)
	if code != 200 || state != "absent" || current.Saved || !current.TransferRetryable {
		t.Fatalf("absence %d %s %+v", code, state, current)
	}
	fake.setDown(true)
	code, state, _ = verifyUploadTest(t, a, p.Task.ID)
	if code != 502 || state == "absent" {
		t.Fatalf("outage was treated as absence: %d %s", code, state)
	}
	fake.setDown(false)
	code, state, _ = verifyUploadTest(t, a, p.Task.ID)
	if code != 200 || state != "absent" {
		t.Fatal("failed verification poisoned task")
	}
}

func TestUploadVerificationKeepsS3CollisionDistinctFromCompletion(t *testing.T) {
	fake := newFakeS3(t, "family")
	a := newUploadTestApp(t, fake)
	p := a.prepare(t, "s3-1", "collision.jpg", 10)
	fake.put("collision.jpg", []byte("other owner"))
	code, state, current := verifyUploadTest(t, a, p.Task.ID)
	if code != 200 || state != "skipped" || current.Saved || current.Phase != "skipped" {
		t.Fatalf("collision %d %s %+v", code, state, current)
	}
}

func TestUploadStreamInterruptionAllowsNewTransferAndPublishesNoPartialFile(t *testing.T) {
	a := newUploadTestApp(t, nil)
	data := uploadTestJPEG(t, color.RGBA{80, 180, 40, 255})
	p := a.prepare(t, "local", "interrupted.jpg", len(data))
	reader, writer := io.Pipe()
	request, _ := http.NewRequest(http.MethodPut, a.server.URL+p.URL, reader)
	request.ContentLength = int64(len(data))
	request.AddCookie(a.cookie)
	go func() { _, _ = writer.Write(data[:len(data)/2]); _ = writer.Close() }()
	response, _ := http.DefaultClient.Do(request)
	if response != nil {
		response.Body.Close()
	}
	current := a.wait(t, p.Task.ID)
	if current.Saved || current.Retryable || !current.TransferRetryable || current.Code != "transfer_failed" {
		t.Fatalf("failed transfer %+v", current)
	}
	code, state, _ := verifyUploadTest(t, a, p.Task.ID)
	if code != 200 || state != "absent" {
		t.Fatalf("verification %d %s", code, state)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.PhotosDir, "interrupted.jpg")); !os.IsNotExist(err) {
		t.Fatal("partial original was published")
	}
	next := a.prepare(t, "local", "interrupted.jpg", len(data))
	response, _ = a.request(t, http.MethodPut, next.URL, data)
	if response.StatusCode != 200 || a.wait(t, next.Task.ID).Phase != "done" {
		t.Fatal("new transfer failed")
	}
	stored, err := os.ReadFile(filepath.Join(a.cfg.PhotosDir, "interrupted.jpg"))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatal("recovered original mismatch")
	}
}

func TestUploadVerificationRejectsInvalidLocalImage(t *testing.T) {
	a := newUploadTestApp(t, nil)
	data := []byte("not a jpeg")
	p := a.prepare(t, "local", "invalid.jpg", len(data))
	response, _ := a.request(t, http.MethodPut, p.URL, data)
	if response.StatusCode != 400 {
		t.Fatal("invalid image accepted")
	}
	code, state, current := verifyUploadTest(t, a, p.Task.ID)
	if code != 200 || state != "rejected" || current.Saved || current.TransferRetryable {
		t.Fatalf("invalid image %d %s %+v", code, state, current)
	}
}

func TestUploadVerificationUsesAuthenticationAndSameOriginJSON(t *testing.T) {
	a := newUploadTestApp(t, nil)
	for _, scenario := range []struct {
		cookie       bool
		origin, kind string
		status       int
	}{
		{false, "", "application/json", 401}, {true, "https://other.example", "application/json", 403},
		{true, "", "application/x-www-form-urlencoded", 415}, {true, "", "application/json", 404},
	} {
		request, _ := http.NewRequest(http.MethodPost, a.server.URL+"/api/uploads/missing/verify", strings.NewReader("{}"))
		if scenario.cookie {
			request.AddCookie(a.cookie)
		}
		request.Header.Set("Origin", scenario.origin)
		request.Header.Set("Content-Type", scenario.kind)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != scenario.status {
			t.Fatalf("scenario %+v got %d", scenario, response.StatusCode)
		}
	}
}
