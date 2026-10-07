package cmd

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
)

func TestRequireYes(t *testing.T) {
	if err := vmRequireYes(true, "do a thing"); err != nil {
		t.Fatalf("vmRequireYes(true) = %v, want nil", err)
	}
	err := vmRequireYes(false, "do a thing")
	if err == nil {
		t.Fatal("vmRequireYes(false) = nil, want error")
	}
	if code := output.ErrorCode(err); code != "CONFIRMATION_REQUIRED" {
		t.Fatalf("error code = %q, want CONFIRMATION_REQUIRED", code)
	}
	if !strings.Contains(err.Error(), "do a thing") {
		t.Fatalf("error %q does not describe the action", err)
	}
}

func TestFlattenRows(t *testing.T) {
	rows := vmFlattenRows("", map[string]any{
		"b": true,
		"a": map[string]any{"x": float64(1), "y": "z"},
		"n": nil,
	})
	want := [][]string{
		{"a.x", "1"},
		{"a.y", "z"},
		{"b", "true"},
		{"n", ""},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i][0] != want[i][0] || rows[i][1] != want[i][1] {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[float64]string{
		0:           "0 B",
		512:         "512 B",
		1024:        "1.00 KiB",
		28801204224: "26.82 GiB",
	}
	for in, want := range cases {
		if got := vmFormatBytes(in); got != want {
			t.Errorf("vmFormatBytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeBackground(t *testing.T) {
	raw := []byte("PNGDATA")
	cases := []string{
		base64.StdEncoding.EncodeToString(raw),
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(raw),
		"",
	}
	for _, in := range cases {
		got, err := decodeBackground(in)
		if err != nil {
			t.Fatalf("decodeBackground(%q) error: %v", in, err)
		}
		if in == "" {
			if len(got) != 0 {
				t.Errorf("decodeBackground(%q) = %q, want empty", in, got)
			}
			continue
		}
		if string(got) != string(raw) {
			t.Errorf("decodeBackground(%q) = %q, want %q", in, got, raw)
		}
	}
}

func TestMultipartFile(t *testing.T) {
	contentType, body, err := vmMultipartFile("file", "wall.png", []byte("hello"))
	if err != nil {
		t.Fatalf("vmMultipartFile error: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type %q: %v", contentType, err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q, want multipart/form-data", mediaType)
	}
	mr := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	part, err := mr.NextPart()
	if err != nil {
		t.Fatalf("next part: %v", err)
	}
	if part.FormName() != "file" || part.FileName() != "wall.png" {
		t.Fatalf("part name/file = %q/%q, want file/wall.png", part.FormName(), part.FileName())
	}
	data, _ := io.ReadAll(part)
	if string(data) != "hello" {
		t.Fatalf("part body = %q, want hello", data)
	}
}

func TestDecodeEnvelope(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var out map[string]any
		if err := vmDecodeEnvelope([]byte(`{"ok":true,"result":{"x":1}}`), 200, &out); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["x"] != float64(1) {
			t.Fatalf("out = %v, want x=1", out)
		}
	})
	t.Run("device error", func(t *testing.T) {
		err := vmDecodeEnvelope([]byte(`{"ok":false,"result":{"error":"BadRequestError","error_msg":"nope"}}`), 200, nil)
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *api.APIError", err)
		}
		if apiErr.Code != "BadRequestError" || apiErr.Message != "nope" {
			t.Fatalf("apiErr = %+v", apiErr)
		}
	})
	t.Run("http error non-envelope", func(t *testing.T) {
		err := vmDecodeEnvelope([]byte("404: Not Found"), 404, nil)
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *api.APIError", err)
		}
		if !apiErr.IsNotFound() {
			t.Fatalf("status = %d, want 404", apiErr.Status)
		}
	})
}

func TestRawPost(t *testing.T) {
	var gotPath, gotToken, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotToken = r.Header.Get("token")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"uploaded":true}}`))
	}))
	defer srv.Close()

	client := api.NewClient(srv.URL, "", "", api.Options{})
	client.SetToken("tok123")

	var out map[string]any
	err := vmRawPost(client, "/api/msd/write?image=x.iso", "application/octet-stream", []byte("BINARY"), 0, &out)
	if err != nil {
		t.Fatalf("vmRawPost error: %v", err)
	}
	if gotPath != "/api/msd/write?image=x.iso" {
		t.Errorf("path = %q", gotPath)
	}
	if gotToken != "tok123" {
		t.Errorf("token = %q, want tok123", gotToken)
	}
	if gotBody != "BINARY" {
		t.Errorf("body = %q, want BINARY", gotBody)
	}
	if out["uploaded"] != true {
		t.Errorf("out = %v, want uploaded=true", out)
	}
}

func TestHandleUnsupported(t *testing.T) {
	err := vmHandleUnsupported(&api.APIError{Status: 404, Code: "Not Found", Message: "404: Not Found"}, "USB switch control", "/api/switch")
	if err == nil || output.ErrorCode(err) != "NOT_SUPPORTED" {
		t.Fatalf("vmHandleUnsupported = %v (code %q), want NOT_SUPPORTED", err, output.ErrorCode(err))
	}
	// A non-404 error must not be reclassified as unsupported.
	other := vmHandleUnsupported(&api.APIError{Status: 500, Code: "ServerError", Message: "boom"}, "x", "/x")
	if output.ErrorCode(other) != "DEVICE_ERROR" {
		t.Fatalf("code = %q, want DEVICE_ERROR", output.ErrorCode(other))
	}
}
