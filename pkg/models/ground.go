package models

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

// Element is one detected UI element from OmniParser.
type Element struct {
	Type          string     `json:"type"`
	Interactivity bool       `json:"interactivity"`
	Content       string     `json:"content"`
	BBox          [4]float64 `json:"bbox"`      // pixels, xyxy
	BBoxNorm      [4]float64 `json:"bbox_norm"` // 0..1, xyxy
	Center        [2]float64 `json:"center"`    // pixels, xy
}

// GroundOptions tunes a Ground call. The zero value requests the documented
// defaults: box_threshold 0.05, iou_threshold 0.1 and the annotated image.
type GroundOptions struct {
	BoxThreshold     float64
	IouThreshold     float64
	IncludeAnnotated bool
}

// GroundResult is the decoded POST /v1/ground response.
//
// AnnotatedImage holds the base64 Set-of-Mark PNG. It is excluded from JSON
// marshalling so callers cannot accidentally stream a large binary blob into a
// model context; persist it to a scratch path instead.
type GroundResult struct {
	Model          string    `json:"model"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Count          int       `json:"count"`
	Elements       []Element `json:"elements"`
	ElapsedMS      float64   `json:"elapsed_ms"`
	AnnotatedImage string    `json:"-"`
}

// groundWire mirrors the wire response, which carries annotated_image.
type groundWire struct {
	Model          string    `json:"model"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Count          int       `json:"count"`
	Elements       []Element `json:"elements"`
	ElapsedMS      float64   `json:"elapsed_ms"`
	AnnotatedImage string    `json:"annotated_image"`
}

// Ground reads imagePath and forwards it to the grounding model's /v1/ground.
func (c *Client) Ground(ctx context.Context, imagePath string, opts GroundOptions) (*GroundResult, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, redact.Error(fmt.Errorf("models: read image %s: %w", imagePath, err))
	}
	return c.GroundBytes(ctx, data, filepath.Base(imagePath), opts)
}

// GroundBytes is Ground for an already-loaded image.
func (c *Client) GroundBytes(ctx context.Context, data []byte, filename string, opts GroundOptions) (*GroundResult, error) {
	body, contentType, err := buildGroundForm(data, filename, opts)
	if err != nil {
		return nil, err
	}
	path := "/model/" + url.PathEscape(c.GroundingModel) + "/v1/ground"

	var wire groundWire
	if err := c.do(ctx, http.MethodPost, path, contentType, body, &wire); err != nil {
		return nil, err
	}
	return &GroundResult{
		Model:          wire.Model,
		Width:          wire.Width,
		Height:         wire.Height,
		Count:          wire.Count,
		Elements:       wire.Elements,
		ElapsedMS:      wire.ElapsedMS,
		AnnotatedImage: wire.AnnotatedImage,
	}, nil
}

// groundFormFields is the exact form field order posted to /v1/ground.
var groundFormFields = []string{
	"box_threshold",
	"iou_threshold",
	"use_paddleocr",
	"imgsz",
	"batch_size",
	"include_annotated",
}

func buildGroundForm(data []byte, filename string, opts GroundOptions) ([]byte, string, error) {
	if filename == "" {
		filename = "image.png"
	}
	box, iou, annotated := groundDefaults(opts)
	values := map[string]string{
		"box_threshold":     strconv.FormatFloat(box, 'f', -1, 64),
		"iou_threshold":     strconv.FormatFloat(iou, 'f', -1, 64),
		"use_paddleocr":     "false",
		"imgsz":             "640",
		"batch_size":        "32",
		"include_annotated": strconv.FormatBool(annotated),
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", redact.Error(fmt.Errorf("models: build multipart: %w", err))
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", redact.Error(fmt.Errorf("models: write multipart file: %w", err))
	}
	for _, k := range groundFormFields {
		if err := w.WriteField(k, values[k]); err != nil {
			return nil, "", redact.Error(fmt.Errorf("models: write multipart field %s: %w", k, err))
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", redact.Error(fmt.Errorf("models: finalize multipart: %w", err))
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// groundDefaults maps the documented option defaults onto a call.
func groundDefaults(opts GroundOptions) (box, iou float64, annotated bool) {
	box = opts.BoxThreshold
	if box <= 0 {
		box = 0.05
	}
	iou = opts.IouThreshold
	if iou <= 0 {
		iou = 0.1
	}
	annotated = opts.IncludeAnnotated
	// A zero-valued GroundOptions means "all defaults", which includes the
	// annotated image. Callers that set any field opt out of that shortcut.
	if opts == (GroundOptions{}) {
		annotated = true
	}
	return box, iou, annotated
}

// ParsedContent is one entry of the Microsoft omniparserserver content list.
type ParsedContent struct {
	Type          string     `json:"type"`
	BBox          [4]float64 `json:"bbox"` // normalized xyxy
	Interactivity bool       `json:"interactivity"`
	Content       string     `json:"content"`
	Source        string     `json:"source"`
}

// ParseResult is the decoded POST /parse/ response.
//
// SOMImageBase64 holds the base64 Set-of-Mark PNG and is excluded from JSON
// marshalling for the same reason as GroundResult.AnnotatedImage.
type ParseResult struct {
	SOMImageBase64 string          `json:"-"`
	ContentList    []ParsedContent `json:"parsed_content_list"`
	Latency        float64         `json:"latency"`
}

type parseRequest struct {
	Base64Image string `json:"base64_image"`
}

type parseWire struct {
	SOMImageBase64 string          `json:"som_image_base64"`
	ContentList    []ParsedContent `json:"parsed_content_list"`
	Latency        float64         `json:"latency"`
}

// Parse reads imagePath and calls the Microsoft omniparserserver compatibility
// endpoint /parse/.
func (c *Client) Parse(ctx context.Context, imagePath string) (*ParseResult, error) {
	raw, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, redact.Error(fmt.Errorf("models: read image %s: %w", imagePath, err))
	}
	body, err := json.Marshal(parseRequest{Base64Image: base64.StdEncoding.EncodeToString(raw)})
	if err != nil {
		return nil, redact.Error(fmt.Errorf("models: encode parse request: %w", err))
	}
	path := "/model/" + url.PathEscape(c.GroundingModel) + "/parse/"

	var wire parseWire
	if err := c.do(ctx, http.MethodPost, path, "application/json", body, &wire); err != nil {
		return nil, err
	}
	return &ParseResult{
		SOMImageBase64: wire.SOMImageBase64,
		ContentList:    wire.ContentList,
		Latency:        wire.Latency,
	}, nil
}
