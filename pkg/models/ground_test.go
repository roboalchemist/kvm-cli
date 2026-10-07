package models

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroundMultipartAndDefaults(t *testing.T) {
	var (
		gotPath     string
		gotFilename string
		gotFile     []byte
		gotForm     map[string]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
		} else {
			gotFilename = hdr.Filename
			gotFile, _ = io.ReadAll(file)
		}
		gotForm = map[string]string{}
		for _, k := range groundFormFields {
			gotForm[k] = r.FormValue(k)
		}
		_, _ = w.Write([]byte(`{
			"model":"omniparser","width":800,"height":600,"count":1,
			"elements":[{"type":"text","interactivity":true,"content":"Sign in","bbox":[1,2,3,4],"bbox_norm":[0.1,0.2,0.3,0.4],"center":[100,120]}],
			"elapsed_ms":12.5,"annotated_image":"QUJD"
		}`))
	}))
	defer srv.Close()

	img := filepath.Join(t.TempDir(), "screen.jpg")
	if err := os.WriteFile(img, []byte("IMAGEBYTES"), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}

	// Zero options must produce the documented defaults.
	res, err := newTestClient(t, srv).Ground(context.Background(), img, GroundOptions{})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}

	if gotPath != "/model/omniparser/v1/ground" {
		t.Errorf("path = %q", gotPath)
	}
	if gotFilename != "screen.jpg" {
		t.Errorf("filename = %q", gotFilename)
	}
	if string(gotFile) != "IMAGEBYTES" {
		t.Errorf("file = %q", gotFile)
	}
	if gotForm["box_threshold"] != "0.05" || gotForm["iou_threshold"] != "0.1" {
		t.Errorf("thresholds = %q/%q", gotForm["box_threshold"], gotForm["iou_threshold"])
	}
	if gotForm["include_annotated"] != "true" {
		t.Errorf("include_annotated = %q", gotForm["include_annotated"])
	}
	if gotForm["use_paddleocr"] != "false" || gotForm["imgsz"] != "640" || gotForm["batch_size"] != "32" {
		t.Errorf("fixed form fields = %+v", gotForm)
	}

	if res.Model != "omniparser" || res.Width != 800 || res.Height != 600 || res.Count != 1 {
		t.Errorf("result envelope = %+v", res)
	}
	if len(res.Elements) != 1 || res.Elements[0].Content != "Sign in" {
		t.Errorf("elements = %+v", res.Elements)
	}
	if res.Elements[0].BBox != [4]float64{1, 2, 3, 4} || res.Elements[0].Center != [2]float64{100, 120} {
		t.Errorf("element geometry = %+v", res.Elements[0])
	}
	if res.ElapsedMS != 12.5 {
		t.Errorf("elapsed_ms = %v", res.ElapsedMS)
	}
	if res.AnnotatedImage != "QUJD" {
		t.Errorf("AnnotatedImage = %q", res.AnnotatedImage)
	}
}

func TestGroundCustomOptions(t *testing.T) {
	var gotForm map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		gotForm = map[string]string{}
		for _, k := range groundFormFields {
			gotForm[k] = r.FormValue(k)
		}
		_, _ = w.Write([]byte(`{"model":"omniparser","count":0,"elements":[]}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GroundBytes(context.Background(), []byte("x"), "in.png", GroundOptions{
		BoxThreshold:     0.3,
		IouThreshold:     0.4,
		IncludeAnnotated: true,
	})
	if err != nil {
		t.Fatalf("GroundBytes: %v", err)
	}
	if gotForm["box_threshold"] != "0.3" || gotForm["iou_threshold"] != "0.4" {
		t.Errorf("thresholds = %q/%q", gotForm["box_threshold"], gotForm["iou_threshold"])
	}
}

func TestGroundDisableAnnotated(t *testing.T) {
	var gotAnnotated string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		gotAnnotated = r.FormValue("include_annotated")
		_, _ = w.Write([]byte(`{"model":"omniparser","count":0,"elements":[]}`))
	}))
	defer srv.Close()

	// Setting at least one field opts out of the all-defaults shortcut, so the
	// false value is honored.
	_, err := newTestClient(t, srv).GroundBytes(context.Background(), []byte("x"), "in.png", GroundOptions{
		BoxThreshold: 0.05, IouThreshold: 0.1, IncludeAnnotated: false,
	})
	if err != nil {
		t.Fatalf("GroundBytes: %v", err)
	}
	if gotAnnotated != "false" {
		t.Errorf("include_annotated = %q, want false", gotAnnotated)
	}
}

func TestGroundDefaultsHelper(t *testing.T) {
	box, iou, annotated := groundDefaults(GroundOptions{})
	if box != 0.05 || iou != 0.1 || !annotated {
		t.Errorf("zero defaults = %v/%v/%v", box, iou, annotated)
	}
	box, iou, annotated = groundDefaults(GroundOptions{BoxThreshold: -1, IouThreshold: -2})
	if box != 0.05 || iou != 0.1 || annotated {
		t.Errorf("negative defaults = %v/%v/%v", box, iou, annotated)
	}
}

func TestGroundBytesDefaultFilename(t *testing.T) {
	var gotFilename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		_, hdr, _ := r.FormFile("file")
		gotFilename = hdr.Filename
		_, _ = w.Write([]byte(`{"model":"omniparser","count":0,"elements":[]}`))
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).GroundBytes(context.Background(), []byte("x"), "", GroundOptions{}); err != nil {
		t.Fatalf("GroundBytes: %v", err)
	}
	if gotFilename != "image.png" {
		t.Errorf("filename = %q, want image.png", gotFilename)
	}
}

func TestGroundReadError(t *testing.T) {
	c := NewClient(Options{BaseURL: "http://base.example"})
	if _, err := c.Ground(context.Background(), filepath.Join(t.TempDir(), "missing.jpg"), GroundOptions{}); err == nil {
		t.Fatal("expected read error")
	}
}

func TestGroundHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad image"}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).GroundBytes(context.Background(), []byte("x"), "in.png", GroundOptions{})
	if err == nil || !strings.Contains(err.Error(), "bad image") {
		t.Fatalf("err = %v", err)
	}
}

func TestParse(t *testing.T) {
	payload := []byte("PNGDATA")
	var gotRequest parseRequest
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{
			"som_image_base64":"U09N","parsed_content_list":[
				{"type":"text","bbox":[0.1,0.2,0.3,0.4],"interactivity":true,"content":"OK","source":"box_ocr_content_ocr"}
			],"latency":0.25
		}`))
	}))
	defer srv.Close()

	img := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(img, payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := newTestClient(t, srv).Parse(context.Background(), img)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if gotPath != "/model/omniparser/parse/" {
		t.Errorf("path = %q", gotPath)
	}
	if want := base64.StdEncoding.EncodeToString(payload); gotRequest.Base64Image != want {
		t.Errorf("base64_image = %q, want %q", gotRequest.Base64Image, want)
	}
	if res.SOMImageBase64 != "U09N" {
		t.Errorf("SOMImageBase64 = %q", res.SOMImageBase64)
	}
	if res.Latency != 0.25 {
		t.Errorf("latency = %v", res.Latency)
	}
	if len(res.ContentList) != 1 || res.ContentList[0].Source != "box_ocr_content_ocr" || !res.ContentList[0].Interactivity {
		t.Errorf("content list = %+v", res.ContentList)
	}
	if res.ContentList[0].BBox != [4]float64{0.1, 0.2, 0.3, 0.4} {
		t.Errorf("bbox = %v", res.ContentList[0].BBox)
	}
}

func TestParseReadError(t *testing.T) {
	c := NewClient(Options{BaseURL: "http://base.example"})
	if _, err := c.Parse(context.Background(), filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Fatal("expected read error")
	}
}

func TestAnnotatedImagesExcludedFromJSON(t *testing.T) {
	ground, err := json.Marshal(GroundResult{AnnotatedImage: "SECRETB64", Model: "omniparser"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(ground), "SECRETB64") || strings.Contains(string(ground), "annotated_image") {
		t.Errorf("GroundResult leaked the annotated image: %s", ground)
	}

	parsed, err := json.Marshal(ParseResult{SOMImageBase64: "SECRETB64"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(parsed), "SECRETB64") || strings.Contains(string(parsed), "som_image_base64") {
		t.Errorf("ParseResult leaked the SOM image: %s", parsed)
	}
}
