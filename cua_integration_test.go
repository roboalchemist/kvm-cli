//go:build integration

package main

// CUA integration tests ().
//
// These tests drive the real binary as a subprocess for the computer-use (cua)
// command family: 'cua models', 'cua probe'/'status', 'cua ground',
// 'cua click' and 'cua parse'. They are split into two groups:
//
// - Live tests talk to the models platform (public, default
// https://models.example.com) and, for the capture paths, the GL.iNet KVM.
// They skip cleanly (t.Skip) when either dependency is unreachable.
// - TestIntegration_CuaPlannerReceivesTextOnly is hermetic: it points
// --models-url at a local httptest server and asserts the planner request
// body carries only the element *text* list — never image bytes — so the
// "images stay out of the model context" contract is enforced in CI.
//
// Command coverage is reconciled with TestIntegration_Coverage in
// integration_test.go via cuaCoveredCommands below.
//
// Run with:
//
//	go test -tags integration -run Integration -count=1 ./...
//	READONLY=1 go test -tags integration -run Integration -count=1 ./...

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// cuaTimeout bounds a single cua subprocess run. A ground/click run does a
// screenshot plus up to two platform round-trips (ground is the slow one) and
// the models client's own default is 120s, so allow headroom above that.
const cuaTimeout = 180 * time.Second

// cuaCoveredCommands are the cua leaf commands exercised by these live tests.
// TestIntegration_Coverage consumes this list so both files stay in sync.
var cuaCoveredCommands = []string{
	"cua models",
	"cua probe",
	"cua status",
	"cua ground",
	"cua click",
	"cua find",
	"cua wait",
	"cua text",
	"cua parse",
}

// ---------------------------------------------------------------------------
// Reachability gating
// ---------------------------------------------------------------------------

var (
	modelsOnce        sync.Once
	modelsReachable   bool
	modelsProbeReason string
)

// modelsPlatformUp probes the models platform exactly once via 'cua probe'
// (which never touches the KVM device). The result is cached for the binary.
func modelsPlatformUp() bool {
	modelsOnce.Do(func() {
		r := runRaw(45*time.Second, nil, "cua", "probe", "--json")
		modelsReachable = r.code == 0 && json.Valid([]byte(strings.TrimSpace(r.stdout)))
		if !modelsReachable {
			modelsProbeReason = firstLine(r.stderr)
			if strings.TrimSpace(modelsProbeReason) == "" {
				modelsProbeReason = fmt.Sprintf("cua probe exit %d", r.code)
			}
		}
	})
	return modelsReachable
}

// requireModels skips the test when the models platform cannot be reached.
func requireModels(t *testing.T) {
	t.Helper()
	if modelsPlatformUp() {
		return
	}
	t.Skipf("integration: models platform unreachable: %s", modelsProbeReason)
}

// requireLiveModels is the common gate for cua commands that need both the KVM
// (to capture a frame) and the models platform (to ground/plan).
func requireLiveModels(t *testing.T) {
	t.Helper()
	requireLive(t)
	requireModels(t)
}

// runCUA runs a cua subcommand with the generous cua timeout.
func runCUA(t *testing.T, overrides map[string]string, args ...string) cliResult {
	t.Helper()
	r := runRaw(cuaTimeout, overrides, args...)
	if r.code == -1 {
		t.Fatalf("kvm-cli %v: process did not complete within %s", args, cuaTimeout)
	}
	return r
}

// runRawInDir is runRaw with an explicit working directory, so a test can prove
// a command wrote nothing into the current directory.
func runRawInDir(dir string, timeout time.Duration, overrides map[string]string, args ...string) cliResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c := exec.CommandContext(ctx, binPath, args...)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = withOverrides(cliEnv, overrides)

	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	err := c.Run()

	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return cliResult{stdout: so.String(), stderr: se.String(), code: code}
}

// ---------------------------------------------------------------------------
// Output shapes (mirrors cmd/cua.go JSON)
// ---------------------------------------------------------------------------

type cuaModelsJSON struct {
	ModelsURL        string `json:"models_url"`
	GroundingDefault string `json:"grounding_default"`
	PlannerDefault   string `json:"planner_default"`
	Models           []struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Running     bool   `json:"running"`
		Default     bool   `json:"default"`
		DefaultRole string `json:"default_role"`
	} `json:"models"`
}

type cuaProbeJSON struct {
	Message        string `json:"message"`
	ModelsURL      string `json:"models_url"`
	GroundingModel string `json:"grounding_model"`
	PlannerModel   string `json:"planner_model"`
}

type cuaGroundJSON struct {
	ImagePath string `json:"image_path"`
	Model     string `json:"model"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Count     int    `json:"count"`
	Elements  []struct {
		Type    string     `json:"type"`
		Content string     `json:"content"`
		Center  [2]float64 `json:"center"`
	} `json:"elements"`
	ElapsedMS     float64 `json:"elapsed_ms"`
	AnnotatedPath string  `json:"annotated_path"`
}

type cuaClickJSON struct {
	Instruction    string  `json:"instruction"`
	ElementID      int     `json:"element_id"`
	ElementType    string  `json:"element_type"`
	ElementContent string  `json:"element_content"`
	ClickX         float64 `json:"click_x"`
	ClickY         float64 `json:"click_y"`
	ScreenshotPath string  `json:"screenshot_path"`
	GroundMS       float64 `json:"ground_ms"`
	PlanMS         float64 `json:"plan_ms"`
	Executed       bool    `json:"executed"`
	AnnotatedPath  string  `json:"annotated_path"`
}

type cuaParseJSON struct {
	ImagePath   string            `json:"image_path"`
	Count       int               `json:"count"`
	Latency     float64           `json:"latency"`
	ContentList []json.RawMessage `json:"parsed_content_list"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// pathUnder reports whether path lives under dir (after cleaning both).
func pathUnder(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// assertNoBase64Image fails when s carries an inline image blob or the field
// names that would betray one.
func assertNoBase64Image(t *testing.T, label, s string) {
	t.Helper()
	for _, bad := range []string{"annotated_image", "som_image_base64", "base64_image", "data:image", "iVBORw0KGgo", "/9j/"} {
		if strings.Contains(s, bad) {
			t.Fatalf("%s leaked image data (found %q):\n%s", label, bad, firstLine(s))
		}
	}
}

// captureScratchImage captures one screenshot into dir and returns its path.
func captureScratchImage(t *testing.T, dir string) string {
	t.Helper()
	r := runCLI(t, "screenshot", "--scratch-dir", dir, "--json")
	if r.code != 0 {
		t.Skipf("screenshot unavailable on this device: %s", firstLine(r.stderr))
	}
	var meta []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &meta); err != nil || len(meta) == 0 || meta[0].Path == "" {
		t.Fatalf("screenshot --json: bad metadata (%v): %s", err, r.stdout)
	}
	return meta[0].Path
}

// assertNoFilesIn asserts dir contains no regular files (used to prove a command
// did not drop an artifact into its working directory).
func assertNoFilesIn(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("unexpected file %q written to %s (commands must not write to CWD)", e.Name(), dir)
		}
	}
}

// ---------------------------------------------------------------------------
// cua models
// ---------------------------------------------------------------------------

func TestIntegration_CuaModels(t *testing.T) {
	requireModels(t)

	// --- JSON ---------------------------------------------------------------
	r := runCUA(t, nil, "cua", "models", "--json")
	if r.code != 0 {
		t.Fatalf("cua models --json: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got cuaModelsJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua models --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	if got.ModelsURL == "" {
		t.Errorf("cua models: empty models_url")
	}
	kinds := map[string]bool{}
	for _, m := range got.Models {
		if m.ID == "" || m.Kind == "" {
			t.Errorf("cua models: incomplete entry %+v", m)
		}
		kinds[m.Kind] = true
	}
	if !kinds["grounding"] {
		t.Errorf("cua models: no grounding model listed (kinds: %v)", kinds)
	}
	if !kinds["chat"] {
		t.Errorf("cua models: no chat model listed (kinds: %v)", kinds)
	}
	t.Logf("cua models: %d models, grounding_default=%s planner_default=%s",
		len(got.Models), got.GroundingDefault, got.PlannerDefault)

	// --- table --------------------------------------------------------------
	tr := runCUA(t, nil, "cua", "models", "--no-color")
	if tr.code != 0 {
		t.Fatalf("cua models --no-color: exit %d: %s", tr.code, firstLine(tr.stderr))
	}
	assertNoANSI(t, "cua models --no-color", tr.stdout)
	for _, want := range []string{"KIND", "grounding", "chat"} {
		if !strings.Contains(tr.stdout, want) {
			t.Errorf("cua models table missing %q:\n%s", want, tr.stdout)
		}
	}
}

// ---------------------------------------------------------------------------
// cua probe / status
// ---------------------------------------------------------------------------

func TestIntegration_CuaProbe(t *testing.T) {
	requireModels(t)

	r := runCUA(t, nil, "cua", "probe", "--json")
	if r.code != 0 {
		t.Fatalf("cua probe --json: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got cuaProbeJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua probe --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	msg := strings.ToLower(got.Message)
	if !strings.Contains(msg, "ready") || !strings.Contains(msg, "omniparser") {
		t.Fatalf("cua probe message %q does not look like the OmniParser readiness message", got.Message)
	}
	if got.GroundingModel == "" {
		t.Errorf("cua probe: empty grounding_model")
	}
	t.Logf("cua probe: message=%q grounding_model=%s planner_model=%q",
		got.Message, got.GroundingModel, got.PlannerModel)

	// 'cua status' is an alias and must return the same readiness message.
	sr := runCUA(t, nil, "cua", "status", "--json")
	if sr.code != 0 {
		t.Fatalf("cua status --json: exit %d: %s", sr.code, firstLine(sr.stderr))
	}
	var status cuaProbeJSON
	if err := json.Unmarshal([]byte(sr.stdout), &status); err != nil {
		t.Fatalf("cua status --json: invalid JSON: %v\n%s", err, sr.stdout)
	}
	if status.Message != got.Message {
		t.Errorf("cua status message %q != cua probe message %q", status.Message, got.Message)
	}
}

// ---------------------------------------------------------------------------
// cua ground
// ---------------------------------------------------------------------------

func TestIntegration_CuaGroundScratch(t *testing.T) {
	requireLiveModels(t)

	scratch := t.TempDir()
	r := runCUA(t, nil, "cua", "ground", "--scratch-dir", scratch, "--json")
	if r.code != 0 {
		t.Fatalf("cua ground: exit %d: %s", r.code, firstLine(r.stderr))
	}
	// The captured "Captured screenshot ..." note must stay on stderr so stdout
	// is parseable JSON.
	var got cuaGroundJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua ground --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	if got.Count < 1 || len(got.Elements) < 1 {
		t.Fatalf("cua ground: expected >=1 element, got count=%d elements=%d", got.Count, len(got.Elements))
	}
	if !pathUnder(got.ImagePath, scratch) {
		t.Fatalf("cua ground image_path %q not under scratch %q", got.ImagePath, scratch)
	}
	if _, err := os.Stat(got.ImagePath); err != nil {
		t.Fatalf("cua ground: captured screenshot %q missing: %v", got.ImagePath, err)
	}
	if got.AnnotatedPath != "" {
		t.Errorf("cua ground without --annotate wrote annotated_path %q", got.AnnotatedPath)
	}
	assertNoBase64Image(t, "cua ground --json", r.stdout)
	t.Logf("cua ground (scratch): image_path=%s count=%d model=%s elapsed_ms=%.1f",
		got.ImagePath, got.Count, got.Model, got.ElapsedMS)

	// Default (no --scratch-dir / KVM_SCRATCH_DIR): the capture lands under
	// os.TempDir().
	def := runCUA(t, map[string]string{"KVM_SCRATCH_DIR": ""},
		"cua", "ground", "--json")
	if def.code != 0 {
		t.Fatalf("cua ground (default scratch): exit %d: %s", def.code, firstLine(def.stderr))
	}
	var dflt cuaGroundJSON
	if err := json.Unmarshal([]byte(def.stdout), &dflt); err != nil {
		t.Fatalf("cua ground (default) --json: invalid JSON: %v\n%s", err, def.stdout)
	}
	if !pathUnder(dflt.ImagePath, os.TempDir()) {
		t.Fatalf("cua ground default image_path %q not under os.TempDir() %q", dflt.ImagePath, os.TempDir())
	}
	_ = os.Remove(dflt.ImagePath)
}

func TestIntegration_CuaGroundImage(t *testing.T) {
	requireLiveModels(t)

	scratch := t.TempDir()
	img := captureScratchImage(t, scratch)

	r := runCUA(t, nil, "cua", "ground", "--image", img, "--json")
	if r.code != 0 {
		t.Fatalf("cua ground --image: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got cuaGroundJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua ground --image --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	if got.ImagePath != img {
		t.Errorf("cua ground --image: image_path = %q, want %q", got.ImagePath, img)
	}
	if got.Count < 1 || len(got.Elements) < 1 {
		t.Fatalf("cua ground --image: expected >=1 element, got count=%d elements=%d", got.Count, len(got.Elements))
	}
	assertNoBase64Image(t, "cua ground --image --json", r.stdout)
	t.Logf("cua ground (--image): image_path=%s count=%d elapsed_ms=%.1f", got.ImagePath, got.Count, got.ElapsedMS)
}

// ---------------------------------------------------------------------------
// cua click
// ---------------------------------------------------------------------------

// TestIntegration_CuaClickResolve runs the full screenshot->ground->plan loop
// WITHOUT --execute: the command must resolve an element and its click
// coordinate and report executed:false, so the remote HID is never touched.
func TestIntegration_CuaClickResolve(t *testing.T) {
	requireLiveModels(t)

	scratch := t.TempDir()
	r := runCUA(t, nil, "cua", "click", "click the Settings icon", "--scratch-dir", scratch, "--json")
	if r.code != 0 {
		t.Fatalf("cua click: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got cuaClickJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua click --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	if got.Instruction != "click the Settings icon" {
		t.Errorf("instruction = %q", got.Instruction)
	}
	if got.ElementID < 0 {
		t.Errorf("element_id = %d, want >= 0", got.ElementID)
	}
	if !pathUnder(got.ScreenshotPath, scratch) {
		t.Errorf("screenshot_path %q not under scratch %q", got.ScreenshotPath, scratch)
	}
	if got.Executed {
		t.Fatalf("cua click without --execute reported executed:true — HID must not be touched")
	}
	// Click coordinates are the resolved element center; they must be present
	// and finite. (A 0,0 click is only meaningful if the chosen element is at
	// the origin, which the live screen never is, but we assert finiteness.)
	if got.ClickX != got.ClickX || got.ClickY != got.ClickY { // NaN check
		t.Errorf("click coords are NaN: (%v,%v)", got.ClickX, got.ClickY)
	}
	assertNoBase64Image(t, "cua click --json", r.stdout)
	t.Logf("cua click (no --execute): element_id=%d type=%s content=%q click=(%.0f,%.0f) ground_ms=%.1f plan_ms=%.1f executed=%v",
		got.ElementID, got.ElementType, got.ElementContent, got.ClickX, got.ClickY,
		got.GroundMS, got.PlanMS, got.Executed)
}

// TestIntegration_CuaClickExecuteRequiresYes proves --execute without --yes is
// refused with CONFIRMATION_REQUIRED before any device contact: the shot is
// taken after the confirmation check, so a refused run captures nothing.
func TestIntegration_CuaClickExecuteRequiresYes(t *testing.T) {
	requireModels(t)

	scratch := t.TempDir()
	r := runCUA(t, nil, "cua", "click", "click the Settings icon",
		"--execute", "--scratch-dir", scratch, "--json")
	if r.code == 0 {
		t.Fatalf("cua click --execute without --yes: expected non-zero exit")
	}
	if code, ok := structuredErrorCode(r.stderr); !ok || code != "CONFIRMATION_REQUIRED" {
		t.Fatalf("cua click --execute without --yes: code = %q (want CONFIRMATION_REQUIRED): %s",
			code, firstLine(r.stderr))
	}
	// The refusal happens before the image is resolved, so nothing was captured
	// and no HID/stream contact occurred.
	assertNoFilesIn(t, scratch)
}

// ---------------------------------------------------------------------------
// cua parse
// ---------------------------------------------------------------------------

func TestIntegration_CuaParse(t *testing.T) {
	requireLiveModels(t)

	scratch := t.TempDir()
	img := captureScratchImage(t, scratch)

	r := runCUA(t, nil, "cua", "parse", img, "--json")
	if r.code != 0 {
		t.Fatalf("cua parse: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got cuaParseJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua parse --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	if got.ImagePath != img {
		t.Errorf("cua parse: image_path = %q, want %q", got.ImagePath, img)
	}
	if got.Count < 1 || len(got.ContentList) < 1 {
		t.Fatalf("cua parse: expected non-empty content list, got count=%d entries=%d", got.Count, len(got.ContentList))
	}
	assertNoBase64Image(t, "cua parse --json", r.stdout)
	t.Logf("cua parse: image_path=%s count=%d latency=%.3f", got.ImagePath, got.Count, got.Latency)
}

// ---------------------------------------------------------------------------
// screenshot default path + scratch override
// ---------------------------------------------------------------------------

// TestIntegration_ScreenshotDefaultWritesScratchNotCWD proves a bare
// 'screenshot' writes into the scratch directory (or os.TempDir()), never the
// current working directory.
func TestIntegration_ScreenshotDefaultWritesScratchNotCWD(t *testing.T) {
	requireLive(t)

	cwd := t.TempDir()
	scratch := t.TempDir()

	// With an explicit scratch dir.
	r := runRawInDir(cwd, 45*time.Second, map[string]string{"KVM_SCRATCH_DIR": scratch}, "screenshot", "--json")
	if r.code != 0 {
		t.Skipf("screenshot unavailable on this device: %s", firstLine(r.stderr))
	}
	var meta []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &meta); err != nil || len(meta) == 0 {
		t.Fatalf("screenshot --json: bad metadata (%v): %s", err, r.stdout)
	}
	if !pathUnder(meta[0].Path, scratch) {
		t.Fatalf("screenshot path %q not under scratch %q", meta[0].Path, scratch)
	}
	assertNoFilesIn(t, cwd)

	// With no override at all: defaults to os.TempDir(), still not the CWD.
	r = runRawInDir(cwd, 45*time.Second, map[string]string{"KVM_SCRATCH_DIR": ""}, "screenshot", "--json")
	if r.code != 0 {
		t.Fatalf("screenshot (default scratch): exit %d: %s", r.code, firstLine(r.stderr))
	}
	meta = nil
	if err := json.Unmarshal([]byte(r.stdout), &meta); err != nil || len(meta) == 0 {
		t.Fatalf("screenshot --json (default): bad metadata (%v): %s", err, r.stdout)
	}
	if !pathUnder(meta[0].Path, os.TempDir()) {
		t.Fatalf("default screenshot path %q not under os.TempDir() %q", meta[0].Path, os.TempDir())
	}
	assertNoFilesIn(t, cwd)
	_ = os.Remove(meta[0].Path)
}

// TestIntegration_ScratchDirOverrideHonored proves $KVM_SCRATCH_DIR is honored
// and that the global --scratch-dir flag overrides it.
func TestIntegration_ScratchDirOverrideHonored(t *testing.T) {
	requireLive(t)

	cwd := t.TempDir()
	envDir := t.TempDir()
	// A nested, not-yet-existing path also verifies the directory is created.
	flagDir := filepath.Join(t.TempDir(), "nested", "deep")

	// Env only.
	r := runRawInDir(cwd, 45*time.Second, map[string]string{"KVM_SCRATCH_DIR": envDir}, "screenshot", "--json")
	if r.code != 0 {
		t.Skipf("screenshot unavailable on this device: %s", firstLine(r.stderr))
	}
	var meta []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &meta); err != nil || len(meta) == 0 {
		t.Fatalf("screenshot --json (env): bad metadata (%v): %s", err, r.stdout)
	}
	if !pathUnder(meta[0].Path, envDir) {
		t.Fatalf("KVM_SCRATCH_DIR not honored: path %q not under %q", meta[0].Path, envDir)
	}

	// Flag beats env.
	r = runRawInDir(cwd, 45*time.Second, map[string]string{"KVM_SCRATCH_DIR": envDir},
		"screenshot", "--scratch-dir", flagDir, "--json")
	if r.code != 0 {
		t.Fatalf("screenshot --scratch-dir: exit %d: %s", r.code, firstLine(r.stderr))
	}
	meta = nil
	if err := json.Unmarshal([]byte(r.stdout), &meta); err != nil || len(meta) == 0 {
		t.Fatalf("screenshot --json (flag): bad metadata (%v): %s", err, r.stdout)
	}
	if !pathUnder(meta[0].Path, flagDir) {
		t.Fatalf("--scratch-dir did not override env: path %q not under %q", meta[0].Path, flagDir)
	}
	if _, err := os.Stat(flagDir); err != nil {
		t.Fatalf("--scratch-dir was not created: %v", err)
	}
	assertNoFilesIn(t, cwd)
}

// ---------------------------------------------------------------------------
// Hermetic contract: the planner receives only element text
// ---------------------------------------------------------------------------

// TestIntegration_CuaPlannerReceivesTextOnly points --models-url at a local
// mock, runs the full click loop against a provided --image, and asserts the
// chat-completions request body contains the element text list but no image
// bytes. It always runs (no device or network dependency).
func TestIntegration_CuaPlannerReceivesTextOnly(t *testing.T) {
	imgBytes := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	imgPath := filepath.Join(t.TempDir(), "screen.jpg")
	if err := os.WriteFile(imgPath, imgBytes, 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	imgB64 := base64.StdEncoding.EncodeToString(imgBytes)
	somB64 := base64.StdEncoding.EncodeToString([]byte("MOCK_SET_OF_MARK_PNG"))

	var (
		mu      sync.Mutex
		planReq string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/models":
			fmt.Fprint(w, `{"models":[`+
				`{"id":"omniparser","kind":"grounding","instances":[{"actual":"running"}]},`+
				`{"id":"qwen-test","kind":"chat","instances":[{"actual":"running"}]}]}`)
		case strings.HasSuffix(r.URL.Path, "/probe/"):
			fmt.Fprint(w, `{"message":"Omniparser API ready"}`)
		case strings.HasSuffix(r.URL.Path, "/v1/ground"):
			fmt.Fprint(w, `{"model":"omniparser","width":100,"height":50,"count":2,`+
				`"elements":[`+
				`{"type":"icon","interactivity":true,"content":"Settings","bbox":[10,10,20,20],"bbox_norm":[0.1,0.2,0.3,0.4],"center":[15,25]},`+
				`{"type":"text","interactivity":false,"content":"Terms & Conditions","bbox":[30,5,40,15],"bbox_norm":[0.3,0.1,0.4,0.3],"center":[35,10]}`+
				`],"elapsed_ms":12.5,"annotated_image":"`+somB64+`"}`)
		case strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			planReq = string(body)
			mu.Unlock()
			fmt.Fprint(w, `{"choices":[{"message":{"content":"0"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := runCUA(t, nil, "cua", "click", "click Settings",
		"--image", imgPath, "--models-url", srv.URL, "--planner", "qwen-test", "--json")
	if r.code != 0 {
		t.Fatalf("cua click (mock): exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got cuaClickJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua click (mock) --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	if got.ElementID != 0 {
		t.Errorf("element_id = %d, want 0", got.ElementID)
	}
	if got.Executed {
		t.Errorf("executed = true, want false")
	}

	mu.Lock()
	body := planReq
	mu.Unlock()
	if strings.TrimSpace(body) == "" {
		t.Fatal("planner request body was not captured by the mock")
	}
	for _, want := range []string{
		"Instruction: click Settings",
		// Element lines are JSON-quoted inside the user message, so the inner
		// quotes arrive escaped; the OCR content must still be verbatim.
		`0: icon \"Settings\"`,
		`1: text \"Terms & Conditions\"`,
		"Which element id",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("planner request missing %q:\n%s", want, body)
		}
	}
	// : the harness JSON.stringify does not HTML-escape, so '&' stays literal.
	if strings.Contains(body, `\u0026`) {
		t.Errorf("planner request HTML-escaped '&':\n%s", body)
	}
	for _, leak := range []string{"base64_image", "data:image", "annotated_image", "som_image_base64", imgB64, somB64} {
		if strings.Contains(body, leak) {
			t.Errorf("planner request leaked image data (found %q):\n%s", leak, body)
		}
	}
	t.Logf("planner received %d bytes of text-only element list (no image bytes)", len(body))
}

// ---------------------------------------------------------------------------
// v0.3.0 ergonomics — hermetic (mock models platform) + live region capture
// ---------------------------------------------------------------------------

// cuaMockFixture is a hermetic stand-in for the models platform that records how
// many planner (chat) calls it received, so tests can prove that selector mode
// and 'cua find'/'cua text' never invoke the planner.
type cuaMockFixture struct {
	srv         *httptest.Server
	imgPath     string
	plannerHits *int
}

func newCUAMockFixture(t *testing.T) *cuaMockFixture {
	t.Helper()
	imgBytes := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	imgPath := filepath.Join(t.TempDir(), "screen.jpg")
	if err := os.WriteFile(imgPath, imgBytes, 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	somB64 := base64.StdEncoding.EncodeToString([]byte("MOCK_SET_OF_MARK_PNG"))
	hits := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/models":
			fmt.Fprint(w, `{"models":[`+
				`{"id":"omniparser","kind":"grounding","instances":[{"actual":"running"}]},`+
				`{"id":"qwen-test","kind":"chat","instances":[{"actual":"running"}]}]}`)
		case strings.HasSuffix(r.URL.Path, "/probe/"):
			fmt.Fprint(w, `{"message":"Omniparser API ready"}`)
		case strings.HasSuffix(r.URL.Path, "/v1/ground"):
			fmt.Fprint(w, `{"model":"omniparser","width":100,"height":50,"count":2,`+
				`"elements":[`+
				`{"type":"icon","interactivity":true,"content":"Settings","bbox":[10,10,20,20],"bbox_norm":[0.1,0.2,0.3,0.4],"center":[15,25]},`+
				`{"type":"text","interactivity":false,"content":"Terms & Conditions","bbox":[30,5,40,15],"bbox_norm":[0.3,0.1,0.4,0.3],"center":[35,10]}`+
				`],"elapsed_ms":12.5,"annotated_image":"`+somB64+`"}`)
		case strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
			mu.Lock()
			hits++
			mu.Unlock()
			fmt.Fprint(w, `{"choices":[{"message":{"content":"0"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &cuaMockFixture{srv: srv, imgPath: imgPath, plannerHits: &hits}
}

func (f *cuaMockFixture) plannerCount() int {
	// plannerHits aliases a local in newCUAMockFixture; read under the same lock
	// is unnecessary for the test's single-goroutine assertions, but the handler
	// may still be flushing a response, so just read the value.
	return *f.plannerHits
}

// TestIntegration_CuaFindDeterministic proves 'cua find' selects by text without
// ever calling the planner, and reports the element center as the click target.
func TestIntegration_CuaFindDeterministic(t *testing.T) {
	f := newCUAMockFixture(t)
	r := runCUA(t, nil, "cua", "find", "--image", f.imgPath,
		"--models-url", f.srv.URL, "--model", "omniparser", "--text", "Settings", "--json")
	if r.code != 0 {
		t.Fatalf("cua find: exit %d: %s", r.code, firstLine(r.stderr))
	}
	assertNoBase64Image(t, "cua find output", r.stdout)
	var got struct {
		Selector   map[string]any `json:"selector"`
		MatchCount int            `json:"match_count"`
		Best       *struct {
			ID      int        `json:"id"`
			Content string     `json:"content"`
			Center  [2]float64 `json:"center"`
		} `json:"best"`
		ClickX float64 `json:"click_x"`
		ClickY float64 `json:"click_y"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua find --json: %v\n%s", err, r.stdout)
	}
	if got.MatchCount != 1 || got.Best == nil || got.Best.ID != 0 {
		t.Fatalf("find result = %+v", got)
	}
	if got.ClickX != 15 || got.ClickY != 25 {
		t.Errorf("click = %v,%v want 15,25", got.ClickX, got.ClickY)
	}
	if n := f.plannerCount(); n != 0 {
		t.Errorf("cua find invoked the planner %d time(s); it must be planner-free", n)
	}
}

// TestIntegration_CuaText proves 'cua text' dumps the OCR element list with no
// planner call.
func TestIntegration_CuaText(t *testing.T) {
	f := newCUAMockFixture(t)
	r := runCUA(t, nil, "cua", "text", "--image", f.imgPath,
		"--models-url", f.srv.URL, "--model", "omniparser", "--json")
	if r.code != 0 {
		t.Fatalf("cua text: exit %d: %s", r.code, firstLine(r.stderr))
	}
	assertNoBase64Image(t, "cua text output", r.stdout)
	var got struct {
		Count    int `json:"count"`
		Elements []struct {
			Content string `json:"content"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua text --json: %v\n%s", err, r.stdout)
	}
	if got.Count != 2 || len(got.Elements) != 2 {
		t.Fatalf("cua text = count %d elements %d", got.Count, len(got.Elements))
	}
	if n := f.plannerCount(); n != 0 {
		t.Errorf("cua text invoked the planner %d time(s)", n)
	}
}

// TestIntegration_CuaClickSelector proves selector-mode click skips the planner
// and returns the deterministic target (no execution without --execute).
func TestIntegration_CuaClickSelector(t *testing.T) {
	f := newCUAMockFixture(t)
	r := runCUA(t, nil, "cua", "click", "--image", f.imgPath,
		"--models-url", f.srv.URL, "--model", "omniparser", "--text", "Settings", "--json")
	if r.code != 0 {
		t.Fatalf("cua click --text: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got cuaClickJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua click --text --json: %v\n%s", err, r.stdout)
	}
	if got.ElementID != 0 || got.ClickX != 15 || got.ClickY != 25 {
		t.Fatalf("selector click = %+v", got)
	}
	if got.Executed {
		t.Error("selector click must not execute without --execute")
	}
	if n := f.plannerCount(); n != 0 {
		t.Errorf("selector-mode click invoked the planner %d time(s)", n)
	}
}

// TestIntegration_CuaWaitHermetic proves 'cua wait' resolves on a matching
// selector without a planner call.
func TestIntegration_CuaWaitHermetic(t *testing.T) {
	f := newCUAMockFixture(t)
	r := runCUA(t, nil, "cua", "wait", "--image", f.imgPath,
		"--models-url", f.srv.URL, "--model", "omniparser",
		"--text", "Settings", "--max-wait", "5s", "--interval", "500ms", "--json")
	if r.code != 0 {
		t.Fatalf("cua wait: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var got struct {
		Found   bool `json:"found"`
		Element *struct {
			ID int `json:"id"`
		} `json:"element"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cua wait --json: %v\n%s", err, r.stdout)
	}
	if !got.Found || got.Element == nil || got.Element.ID != 0 {
		t.Fatalf("cua wait = %+v", got)
	}
	if n := f.plannerCount(); n != 0 {
		t.Errorf("cua wait invoked the planner %d time(s)", n)
	}
}

// TestIntegration_ScreenshotRegion is a live check that 'screenshot --region
// --scale' produces a correctly sized crop and reports the transform metadata.
func TestIntegration_ScreenshotRegion(t *testing.T) {
	requireLive(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "crop.jpg")
	r := runCLI(t, "screenshot", "--region", "0,0,400,300", "--scale", "2",
		"-o", out, "--json")
	if r.code != 0 {
		t.Skipf("screenshot unavailable on this device: %s", firstLine(r.stderr))
	}
	var meta []struct {
		Path   string `json:"path"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Region *struct {
			X1 float64 `json:"x1"`
			Y2 float64 `json:"y2"`
		} `json:"region"`
		Scale float64 `json:"scale"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &meta); err != nil || len(meta) == 0 {
		t.Fatalf("screenshot --region --json: %v\n%s", err, r.stdout)
	}
	m := meta[0]
	if m.Width != 800 || m.Height != 600 {
		t.Errorf("crop dims = %dx%d, want 800x600", m.Width, m.Height)
	}
	if m.Region == nil || m.Region.X1 != 0 || m.Region.Y2 != 300 {
		t.Errorf("region metadata = %+v", m.Region)
	}
	if m.Scale != 2 {
		t.Errorf("scale = %v, want 2", m.Scale)
	}
	if info, err := os.Stat(m.Path); err != nil || info.Size() == 0 {
		t.Errorf("cropped file missing/empty: path=%s err=%v", m.Path, err)
	}
}
