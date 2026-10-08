package grounding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testCrop() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{200, 100, 50, 255})
		}
	}
	return img
}

func TestHTTPCaptionerBatch(t *testing.T) {
	var gotCount int
	var gotTask string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/caption" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req captionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		gotCount = len(req.Images)
		gotTask = req.Task
		_, _ = w.Write([]byte(`{"captions":["a red square","another"],"error":null}`))
	}))
	defer srv.Close()

	h := &HTTPCaptioner{BaseURL: srv.URL, Client: srv.Client()}
	got, err := h.Caption(context.Background(), []image.Image{testCrop(), testCrop()})
	if err != nil {
		t.Fatalf("Caption: %v", err)
	}
	if gotCount != 2 {
		t.Errorf("crops sent = %d, want 2", gotCount)
	}
	if gotTask != TASK_PROMPT_PLACEHOLDER {
		t.Errorf("task = %q, want the Florence-2 detailed-caption task", gotTask)
	}
	if len(got) != 2 || got[0] != "a red square" {
		t.Errorf("captions = %v", got)
	}
	_ = gotAuth
}

func TestHTTPCaptionerErrors(t *testing.T) {
	// HTTP error surfaces status + body snippet.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("kaput"))
	}))
	defer srv.Close()
	h := &HTTPCaptioner{BaseURL: srv.URL, Client: srv.Client()}
	if _, err := h.Caption(context.Background(), []image.Image{testCrop()}); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %v, want HTTP 500", err)
	}

	// Sidecar-reported error.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"captions":[],"error":"model not loaded yet"}`))
	}))
	defer srv2.Close()
	h2 := &HTTPCaptioner{BaseURL: srv2.URL, Client: srv2.Client()}
	if _, err := h2.Caption(context.Background(), []image.Image{testCrop()}); err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("err = %v, want sidecar error", err)
	}

	// Count mismatch.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"captions":["only one"],"error":null}`))
	}))
	defer srv3.Close()
	h3 := &HTTPCaptioner{BaseURL: srv3.URL, Client: srv3.Client()}
	if _, err := h3.Caption(context.Background(), []image.Image{testCrop(), testCrop()}); err == nil || !strings.Contains(err.Error(), "2 crops") {
		t.Errorf("err = %v, want count mismatch", err)
	}

	// Empty crop list short-circuits (no HTTP call).
	if got, err := h3.Caption(context.Background(), nil); err != nil || got != nil {
		t.Errorf("empty crops = %v, %v; want nil, nil", got, err)
	}
}

func TestFetchCaptionerHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ready":true,"device":"mps","model":"microsoft/Florence-2-base","task":"<MORE_DETAILED_CAPTION>","load_seconds":3.2}`))
	}))
	defer srv.Close()
	h, err := FetchCaptionerHealth(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !h.Ready || h.Device != "mps" || h.Model != "microsoft/Florence-2-base" {
		t.Errorf("health = %+v", h)
	}
	// Unreachable sidecar errors.
	if _, err := FetchCaptionerHealth(context.Background(), "http://127.0.0.1:1"); err == nil {
		t.Error("expected error for unreachable sidecar")
	}
}

// TASK_PROMPT_PLACEHOLDER keeps the batch test honest about the request body.
const TASK_PROMPT_PLACEHOLDER = "<MORE_DETAILED_CAPTION>"

// Verify base64 crops decode as JPEG (transport format contract).
func TestCaptionJPEGEncoding(t *testing.T) {
	b64, err := captionJPEG(testCrop())
	if err != nil {
		t.Fatalf("captionJPEG: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(string(raw[:3]), "\xff\xd8\xff") {
		t.Error("crop is not a JPEG")
	}
	if _, err := jpeg.Decode(strings.NewReader(string(raw))); err != nil {
		t.Errorf("crop does not decode: %v", err)
	}
}

// TestTruncateCoversLongInput exercises the truncation branch.
func TestTruncateCoversLongInput(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := truncate(long, 200)
	if len(got) != 203 || !strings.HasSuffix(got, "...") {
		t.Errorf("truncate = len %d, want 203 with ellipsis", len(got))
	}
	if truncate("short", 200) != "short" {
		t.Error("short input should pass through")
	}
}

// TestFetchCaptionerHealthDecodeError covers malformed health JSON.
func TestFetchCaptionerHealthDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	if _, err := FetchCaptionerHealth(context.Background(), srv.URL); err == nil {
		t.Fatal("expected decode error")
	}
	// Invalid URL errors at request construction.
	if _, err := FetchCaptionerHealth(context.Background(), "http://exa mple.com"); err == nil {
		t.Fatal("expected request error for invalid URL")
	}
}
