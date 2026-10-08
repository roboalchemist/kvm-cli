package grounding

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"time"
)

// Captioner turns image crops into text captions.
type Captioner interface {
	Caption(ctx context.Context, crops []image.Image) ([]string, error)
}

// taskPrompt is the Florence-2 captioning task used by OmniParser-v2. It is
// sent explicitly so the contract is visible on both sides of the wire.
const taskPrompt = "<MORE_DETAILED_CAPTION>"

// captionRequest is the sidecar wire request.
type captionRequest struct {
	Images       []string `json:"images"` // base64 JPEG crops
	Task         string   `json:"task,omitempty"`
	MaxNewTokens int      `json:"max_new_tokens,omitempty"`
}

// captionResponse is the sidecar wire response.
type captionResponse struct {
	Captions []string `json:"captions"`
	Error    *string  `json:"error"`
}

// HTTPCaptioner calls the local Florence-2 captioning sidecar.
type HTTPCaptioner struct {
	// BaseURL is the sidecar root, e.g. http://127.0.0.1:8618.
	BaseURL string
	// Client is the HTTP client. Nil uses a 10-minute default (captioning a
	// large batch on CPU is slow).
	Client *http.Client
}

// captionJPEG encodes one crop for transport.
func captionJPEG(img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return "", fmt.Errorf("encode crop: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// Caption implements Captioner.
func (h *HTTPCaptioner) Caption(ctx context.Context, crops []image.Image) ([]string, error) {
	if len(crops) == 0 {
		return nil, nil
	}
	images := make([]string, 0, len(crops))
	for i, c := range crops {
		b64, err := captionJPEG(c)
		if err != nil {
			return nil, fmt.Errorf("crop %d: %w", i, err)
		}
		images = append(images, b64)
	}
	body, err := json.Marshal(captionRequest{Images: images, Task: taskPrompt, MaxNewTokens: 64})
	if err != nil {
		return nil, fmt.Errorf("encode caption request: %w", err)
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(h.BaseURL, "/")+"/caption", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create caption request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("caption sidecar: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read caption response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("caption sidecar: HTTP %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var out captionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode caption response: %w", err)
	}
	if out.Error != nil && *out.Error != "" {
		return nil, fmt.Errorf("caption sidecar: %s", *out.Error)
	}
	if len(out.Captions) != len(crops) {
		return nil, fmt.Errorf("caption sidecar returned %d captions for %d crops",
			len(out.Captions), len(crops))
	}
	return out.Captions, nil
}

// CaptionerHealth is the /health payload of the sidecar.
type CaptionerHealth struct {
	Ready       bool    `json:"ready"`
	Device      string  `json:"device,omitempty"`
	Model       string  `json:"model,omitempty"`
	Task        string  `json:"task,omitempty"`
	Error       *string `json:"error,omitempty"`
	LoadSeconds float64 `json:"load_seconds,omitempty"`
}

// CaptionerHealth fetches the sidecar health endpoint.
func FetchCaptionerHealth(ctx context.Context, baseURL string) (*CaptionerHealth, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out CaptionerHealth
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode health: %w", err)
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
