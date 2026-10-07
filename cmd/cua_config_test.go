package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/auth"
	"github.com/roboalchemist/kvm-cli/pkg/output"
)

// cuaAnnotated are the bytes the mock grounding server returns as the
// base64-encoded Set-of-Mark image. The base64 string is distinctive so a leak
// into JSON output is unambiguous.
var cuaAnnotated = []byte("CUA_PNG_DATA")

func cuaAnnotatedB64() string { return base64.StdEncoding.EncodeToString(cuaAnnotated) }

// saveCUAGlobals snapshots the cua/scratch flag globals the tests mutate.
func saveCUAGlobals(t *testing.T) {
	t.Helper()
	modelsURL := flagCuaModelsURL
	image := flagCuaImage
	model := flagCuaModel
	planner := flagCuaPlanner
	box, iou := flagCuaBox, flagCuaIoU
	annotate := flagCuaAnnotate
	keep := flagCuaKeepImage
	execute, yes := flagCuaExecute, flagCuaYes
	scratch := flagScratchDir
	dryRun := flagDryRun
	t.Cleanup(func() {
		flagCuaModelsURL = modelsURL
		flagCuaImage = image
		flagCuaModel = model
		flagCuaPlanner = planner
		flagCuaBox, flagCuaIoU = box, iou
		flagCuaAnnotate = annotate
		flagCuaKeepImage = keep
		flagCuaExecute, flagCuaYes = execute, yes
		flagScratchDir = scratch
		flagDryRun = dryRun
	})
}

// cuaModelsServer is a hermetic stand-in for the models platform covering the
// catalog, probe, ground, parse and chat-completions endpoints.
func cuaModelsServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/models":
			fmt.Fprint(w, `{"models":[`+
				`{"id":"omniparser","kind":"grounding","instances":[{"actual":"running"}]},`+
				`{"id":"hemmingway","kind":"chat","instances":[{"actual":"running"}]},`+
				`{"id":"qwen-test","kind":"chat","instances":[{"actual":"running"}]}]}`)
		case strings.HasSuffix(r.URL.Path, "/probe/"):
			fmt.Fprint(w, `{"message":"Omniparser API ready"}`)
		case strings.HasSuffix(r.URL.Path, "/v1/ground"):
			fmt.Fprint(w, `{"model":"omniparser","width":100,"height":50,"count":2,`+
				`"elements":[`+
				`{"type":"icon","interactivity":true,"content":"Settings","bbox":[10,10,20,20],"bbox_norm":[0.1,0.2,0.3,0.4],"center":[15,25]},`+
				`{"type":"text","interactivity":false,"content":"Hello & welcome","bbox":[30,5,40,15],"bbox_norm":[0.3,0.1,0.4,0.3],"center":[35,10]}`+
				`],"elapsed_ms":12.5,"annotated_image":"`+cuaAnnotatedB64()+`"}`)
		case strings.HasSuffix(r.URL.Path, "/parse/"):
			fmt.Fprint(w, `{"som_image_base64":"`+cuaAnnotatedB64()+`",`+
				`"parsed_content_list":[{"type":"text","bbox":[0.1,0.2,0.3,0.4],"interactivity":false,"content":"Hello","source":"box_ocr"}],`+
				`"latency":0.42}`)
		case strings.HasSuffix(r.URL.Path, "/v1/chat/completions"):
			fmt.Fprint(w, `{"choices":[{"message":{"content":"0"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func writeTestImage(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "screen.jpg")
	if err := os.WriteFile(path, []byte{0xFF, 0xD8, 0xFF, 0xD9}, 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	return path
}

// cuaSetup isolates HOME/env and resets the cua flag globals for a hermetic run.
func cuaSetup(t *testing.T, srvURL string) {
	t.Helper()
	saveGlobals(t)
	saveCUAGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("KVM_MODELS_URL", "")
	t.Setenv("KVM_GROUNDING_MODEL", "")
	t.Setenv("KVM_PLANNER_MODEL", "")
	t.Setenv("KVM_SCRATCH_DIR", "")
	flagCuaModelsURL = srvURL
	flagCuaImage = ""
	flagCuaModel = ""
	flagCuaPlanner = ""
	flagCuaBox, flagCuaIoU = 0.05, 0.1
	flagCuaAnnotate = ""
	flagCuaKeepImage = true
	flagCuaExecute, flagCuaYes = false, false
	flagScratchDir = ""
	flagDryRun = false
	flagFormat, flagJSON, flagPlaintext = "table", false, false
}

func TestCuaModelsMarksDefaults(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)

	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaModels(cuaModelsCmd, nil); err != nil {
			t.Fatalf("runCuaModels: %v", err)
		}
	})

	var got cuaModelsOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua models --json is not valid JSON: %v\n%s", err, out)
	}
	if got.GroundingDefault != "omniparser" {
		t.Errorf("grounding_default = %q, want omniparser", got.GroundingDefault)
	}
	if got.PlannerDefault != "qwen-test" {
		t.Errorf("planner_default = %q, want qwen-test (catalog auto-pick)", got.PlannerDefault)
	}
	roles := map[string]string{}
	for _, m := range got.Models {
		roles[m.ID] = m.DefaultRole
	}
	if roles["omniparser"] != "grounding" {
		t.Errorf("omniparser role = %q, want grounding", roles["omniparser"])
	}
	if roles["qwen-test"] != "planner" {
		t.Errorf("qwen-test role = %q, want planner", roles["qwen-test"])
	}
}

func TestCuaProbe(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)

	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaProbe(cuaProbeCmd, nil); err != nil {
			t.Fatalf("runCuaProbe: %v", err)
		}
	})
	var got cuaProbeOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua probe --json is not valid JSON: %v\n%s", err, out)
	}
	if got.Message != "Omniparser API ready" {
		t.Errorf("probe message = %q", got.Message)
	}
	if got.GroundingModel != "omniparser" {
		t.Errorf("grounding_model = %q, want omniparser", got.GroundingModel)
	}
}

func TestCuaGroundJSONOmitsBase64(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	img := writeTestImage(t)
	flagCuaImage = img
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaGround(cuaGroundCmd, nil); err != nil {
			t.Fatalf("runCuaGround: %v", err)
		}
	})
	if strings.Contains(out, cuaAnnotatedB64()) {
		t.Fatalf("cua ground --json leaked the annotated base64 image:\n%s", out)
	}
	var got cuaGroundOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua ground --json is not valid JSON: %v\n%s", err, out)
	}
	if got.Count != 2 || len(got.Elements) != 2 {
		t.Errorf("count/elements = %d/%d, want 2/2", got.Count, len(got.Elements))
	}
	if got.ImagePath != img {
		t.Errorf("image_path = %q, want %q", got.ImagePath, img)
	}
}

func TestCuaGroundAnnotateWritesFile(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	flagCuaImage = writeTestImage(t)

	dest := filepath.Join(t.TempDir(), "som.png")
	flagCuaAnnotate = dest
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaGround(cuaGroundCmd, nil); err != nil {
			t.Fatalf("runCuaGround: %v", err)
		}
	})
	var got cuaGroundOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.AnnotatedPath != dest {
		t.Errorf("annotated_path = %q, want %q", got.AnnotatedPath, dest)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read annotated file: %v", err)
	}
	if string(data) != string(cuaAnnotated) {
		t.Errorf("annotated file = %q, want %q", data, cuaAnnotated)
	}

	// --annotate auto writes to a generated scratch path.
	scratch := t.TempDir()
	t.Setenv("KVM_SCRATCH_DIR", scratch)
	flagCuaAnnotate = "auto"
	out = captureStdout(t, func() {
		if err := runCuaGround(cuaGroundCmd, nil); err != nil {
			t.Fatalf("runCuaGround (auto annotate): %v", err)
		}
	})
	got = cuaGroundOutput{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !strings.HasPrefix(got.AnnotatedPath, scratch) {
		t.Errorf("auto annotated path %q not under scratch %q", got.AnnotatedPath, scratch)
	}
}

func TestCuaClickResolvesElement(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	flagCuaImage = writeTestImage(t)
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaClick(cuaClickCmd, []string{"click the Settings icon"}); err != nil {
			t.Fatalf("runCuaClick: %v", err)
		}
	})
	var got cuaClickOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua click --json is not valid JSON: %v\n%s", err, out)
	}
	if got.ElementID != 0 {
		t.Errorf("element_id = %d, want 0", got.ElementID)
	}
	if got.ClickX != 15 || got.ClickY != 25 {
		t.Errorf("click = (%v,%v), want (15,25)", got.ClickX, got.ClickY)
	}
	if got.Executed {
		t.Error("executed = true, want false without --execute")
	}
}

func TestCuaClickExecuteRequiresYes(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	flagCuaImage = writeTestImage(t)
	flagCuaExecute = true
	flagCuaYes = false

	err := runCuaClick(cuaClickCmd, []string{"click Settings"})
	if err == nil {
		t.Fatal("expected CONFIRMATION_REQUIRED with --execute and no --yes")
	}
	if code := output.ErrorCode(err); code != "CONFIRMATION_REQUIRED" {
		t.Fatalf("error code = %q, want CONFIRMATION_REQUIRED (%v)", code, err)
	}
}

func TestCuaParse(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	img := writeTestImage(t)
	flagCuaImage = img
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaParse(cuaParseCmd, nil); err != nil {
			t.Fatalf("runCuaParse: %v", err)
		}
	})
	if strings.Contains(out, cuaAnnotatedB64()) {
		t.Fatalf("cua parse --json leaked the SOM base64 image:\n%s", out)
	}
	var got cuaParseOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua parse --json is not valid JSON: %v\n%s", err, out)
	}
	if got.Count != 1 || len(got.Content) != 1 {
		t.Errorf("count/content = %d/%d, want 1/1", got.Count, len(got.Content))
	}
	if got.ImagePath != img {
		t.Errorf("image_path = %q, want %q", got.ImagePath, img)
	}
}

// TestCuaResolveImageRejectsBoth is a small usage-guard regression.
func TestCuaResolveImageRejectsBoth(t *testing.T) {
	cuaSetup(t, "http://127.0.0.1:1")
	_, _, err := cuaResolveImage(cuaGroundCmd, []string{"a.jpg"}, "b.jpg")
	if code := output.ErrorCode(err); code != "USAGE" {
		t.Fatalf("code = %q, want USAGE (%v)", code, err)
	}
}

// TestScratchDirPrecedence verifies flag > env > config > temp.
func TestScratchDirPrecedence(t *testing.T) {
	saveCUAGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("KVM_SCRATCH_DIR", "")

	cfgDir := filepath.Join(t.TempDir(), "from-config")
	envDir := filepath.Join(t.TempDir(), "from-env")
	flagDir := filepath.Join(t.TempDir(), "from-flag")

	if err := auth.SaveConfig(&auth.Config{ScratchDir: cfgDir}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got, err := ScratchDir()
	if err != nil {
		t.Fatalf("ScratchDir (config): %v", err)
	}
	if got != cfgDir {
		t.Errorf("config scratch = %q, want %q", got, cfgDir)
	}

	t.Setenv("KVM_SCRATCH_DIR", envDir)
	if got, _ = ScratchDir(); got != envDir {
		t.Errorf("env scratch = %q, want %q", got, envDir)
	}

	flagScratchDir = flagDir
	if got, _ = ScratchDir(); got != flagDir {
		t.Errorf("flag scratch = %q, want %q", got, flagDir)
	}
}

// TestScreenshotDefaultIsScratch proves the screenshot default path is under
// the scratch directory and never the literal CWD default.
func TestScreenshotDefaultIsScratch(t *testing.T) {
	saveCUAGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	scratch := t.TempDir()
	t.Setenv("KVM_SCRATCH_DIR", scratch)

	path, err := defaultShotPath()
	if err != nil {
		t.Fatalf("defaultShotPath: %v", err)
	}
	if !strings.HasPrefix(path, scratch) {
		t.Fatalf("default screenshot path %q not under scratch %q", path, scratch)
	}
	if strings.HasSuffix(path, "screenshot.jpg") && !strings.Contains(path, "kvm-screenshot-") {
		t.Fatalf("default path %q looks like the legacy CWD default", path)
	}
	if !strings.Contains(filepath.Base(path), "kvm-screenshot-") {
		t.Errorf("default basename %q lacks the kvm-screenshot prefix", filepath.Base(path))
	}
}

// TestConfigCommandsNewKeys exercises the CLI config surface for the CUA
// keys: set/get/list/unset and the supported-keys list.
func TestConfigCommandsNewKeys(t *testing.T) {
	saveGlobals(t)
	saveCUAGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	for _, k := range []string{"models_url", "grounding_model", "planner_model", "scratch_dir"} {
		found := false
		for _, s := range supportedConfigKeys() {
			if s == k {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("supportedConfigKeys missing %q: %v", k, supportedConfigKeys())
		}
	}

	flagJSON, flagPlaintext, flagFormat = false, false, "table"
	if err := runConfigSet(nil, []string{"scratch_dir", "/tmp/kvmshot"}); err != nil {
		t.Fatalf("config set scratch_dir: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runConfigGet(nil, []string{"scratch_dir"}); err != nil {
			t.Fatalf("config get scratch_dir: %v", err)
		}
	})
	if strings.TrimSpace(out) != "/tmp/kvmshot" {
		t.Fatalf("config get scratch_dir = %q, want /tmp/kvmshot", strings.TrimSpace(out))
	}

	flagJSON = true
	out = captureStdout(t, func() {
		if err := runConfigList(nil, nil); err != nil {
			t.Fatalf("config list: %v", err)
		}
	})
	var listed map[string]string
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("config list --json: %v\n%s", err, out)
	}
	if listed["scratch_dir"] != "/tmp/kvmshot" {
		t.Errorf("config list scratch_dir = %q", listed["scratch_dir"])
	}

	if err := runConfigUnset(nil, []string{"scratch_dir"}); err != nil {
		t.Fatalf("config unset scratch_dir: %v", err)
	}
	out = captureStdout(t, func() {
		if err := runConfigGet(nil, []string{"scratch_dir"}); err != nil {
			t.Fatalf("config get scratch_dir after unset: %v", err)
		}
	})
	if !strings.Contains(out, "(not set)") {
		t.Errorf("config get after unset = %q, want (not set)", out)
	}
}
