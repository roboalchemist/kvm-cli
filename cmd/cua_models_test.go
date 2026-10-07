package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// makeUserinfoURL injects "user:secret" userinfo into a plain http:// URL so a
// test can prove the credentials never reach cua output.
func makeUserinfoURL(srvURL string) string {
	return "http://user:secret@" + strings.TrimPrefix(srvURL, "http://")
}

// TestCuaModelsMasksURLCredentials is the F1 acceptance: a models_url
// carrying userinfo must be masked (https://***@host) in cua models output, in
// both JSON and table/plaintext modes.
func TestCuaModelsMasksURLCredentials(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, makeUserinfoURL(srv.URL))

	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaModels(cuaModelsCmd, nil); err != nil {
			t.Fatalf("runCuaModels: %v", err)
		}
	})
	if strings.Contains(out, "secret") {
		t.Fatalf("cua models --json leaked the URL credential:\n%s", out)
	}
	var got cuaModelsOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua models --json is not valid JSON: %v\n%s", err, out)
	}
	if !strings.Contains(got.ModelsURL, "***@") {
		t.Fatalf("models_url = %q, want a masked userinfo (***@host)", got.ModelsURL)
	}
	if strings.Contains(got.ModelsURL, "secret") || strings.Contains(got.ModelsURL, "user:") {
		t.Fatalf("models_url = %q still carries credentials", got.ModelsURL)
	}

	// Table mode must mask the footer too.
	flagJSON = false
	flagFormat = "table"
	flagNoColor = true
	out = captureStdout(t, func() {
		if err := runCuaModels(cuaModelsCmd, nil); err != nil {
			t.Fatalf("runCuaModels (table): %v", err)
		}
	})
	if strings.Contains(out, "secret") {
		t.Fatalf("cua models table leaked the URL credential:\n%s", out)
	}
	if !strings.Contains(out, "***@") {
		t.Fatalf("cua models table footer lacks the masked URL:\n%s", out)
	}
}

// TestCuaProbeMasksURLAndShowsEffectivePlanner is the F1+F4
// acceptance: cua probe --json masks the URL userinfo and reports the
// auto-resolved (non-"-") effective planner.
func TestCuaProbeMasksURLAndShowsEffectivePlanner(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, makeUserinfoURL(srv.URL))

	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaProbe(cuaProbeCmd, nil); err != nil {
			t.Fatalf("runCuaProbe: %v", err)
		}
	})
	if strings.Contains(out, "secret") {
		t.Fatalf("cua probe --json leaked the URL credential:\n%s", out)
	}
	var got cuaProbeOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua probe --json is not valid JSON: %v\n%s", err, out)
	}
	if !strings.Contains(got.ModelsURL, "***@") || strings.Contains(got.ModelsURL, "secret") {
		t.Fatalf("models_url = %q, want a masked userinfo", got.ModelsURL)
	}
	if got.GroundingModel != "omniparser" {
		t.Errorf("grounding_model = %q, want omniparser", got.GroundingModel)
	}
	if got.PlannerModel != "qwen-test" {
		t.Errorf("planner_model = %q, want the auto-picked qwen-test (not \"-\")", got.PlannerModel)
	}
}

// TestCuaClickDryRunResolvesNoHID is the F6 acceptance: --dry-run
// still runs the resolution loop and prints the target, but never contacts the
// HID, even when --execute --yes are supplied.
func TestCuaClickDryRunResolvesNoHID(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	flagCuaImage = writeTestImage(t)
	flagJSON = true
	flagDryRun = true
	flagCuaExecute = true
	flagCuaYes = true

	out := captureStdout(t, func() {
		if err := runCuaClick(cuaClickCmd, []string{"click the Settings icon"}); err != nil {
			t.Fatalf("runCuaClick --dry-run: %v", err)
		}
	})
	if strings.Contains(out, "secret") {
		t.Fatalf("unexpected secret in output:\n%s", out)
	}
	var got cuaClickOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cua click --dry-run --json is not valid JSON: %v\n%s", err, out)
	}
	if got.Executed {
		t.Error("executed = true under --dry-run; HID must not be touched")
	}
	if !got.DryRun {
		t.Error("dry_run = false under --dry-run; the flag must be reported")
	}
	if got.ElementID != 0 || got.ClickX != 15 || got.ClickY != 25 {
		t.Errorf("resolution wrong: element_id=%d click=(%v,%v), want 0/(15,25)",
			got.ElementID, got.ClickX, got.ClickY)
	}

	// The table form must surface the dry-run state as a row.
	flagJSON = false
	flagFormat = "table"
	flagNoColor = true
	out = captureStdout(t, func() {
		if err := runCuaClick(cuaClickCmd, []string{"click the Settings icon"}); err != nil {
			t.Fatalf("runCuaClick --dry-run (table): %v", err)
		}
	})
	if !strings.Contains(out, "dry_run") || !strings.Contains(out, "false") {
		t.Fatalf("dry-run table output missing executed:false/dry_run:\n%s", out)
	}
}

// TestCuaClickDryRunWithoutExecute also covers the plain --dry-run form.
func TestCuaClickDryRunWithoutExecute(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	flagCuaImage = writeTestImage(t)
	flagJSON = true
	flagDryRun = true

	out := captureStdout(t, func() {
		if err := runCuaClick(cuaClickCmd, []string{"click Settings"}); err != nil {
			t.Fatalf("runCuaClick --dry-run: %v", err)
		}
	})
	var got cuaClickOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Executed || !got.DryRun {
		t.Errorf("executed=%v dry_run=%v, want false/true", got.Executed, got.DryRun)
	}
}

// TestCuaClickExecuteWithoutYesRefuses keeps the F6 guard honest: without
// --dry-run, --execute and no --yes is refused before any resolution.
func TestCuaClickExecuteWithoutYesRefuses(t *testing.T) {
	cuaSetup(t, "http://127.0.0.1:1")
	flagCuaExecute = true
	flagCuaYes = false
	flagDryRun = false

	err := runCuaClick(cuaClickCmd, []string{"click Settings"})
	if err == nil {
		t.Fatal("expected CONFIRMATION_REQUIRED")
	}
	if got := output.ErrorCode(err); got != "CONFIRMATION_REQUIRED" {
		t.Fatalf("code = %q, want CONFIRMATION_REQUIRED (%v)", got, err)
	}
}

// TestModels5xxMapsToSystemExit is the F3 acceptance: a models
// platform 5xx maps to the system exit code (3), not exit 1.
func TestModels5xxMapsToSystemExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer srv.Close()
	cuaSetup(t, srv.URL)

	_, err := cuaModelsClient().Catalog(context.Background())
	if err == nil {
		t.Fatal("expected a 503 error")
	}
	if got := output.ErrorCode(err); got != "DEVICE_ERROR" {
		t.Fatalf("code = %q, want DEVICE_ERROR (%v)", got, err)
	}
	if got := exitCode(err); got != 3 {
		t.Fatalf("exitCode = %d, want 3 (%v)", got, err)
	}
}

// TestModelsTransportErrorMapsToSystemExit covers the transport half of F3.
func TestModelsTransportErrorMapsToSystemExit(t *testing.T) {
	cuaSetup(t, "http://127.0.0.1:1")
	_, err := cuaModelsClient().Catalog(context.Background())
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if got := exitCode(err); got != 3 {
		t.Fatalf("exitCode = %d, want 3 (%v)", got, err)
	}
}

// newGroundingServer records the grounding request path and reports a running
// grounding model other than omniparser, so an auto-pick is observable.
func newGroundingServer(t *testing.T, gotPath *string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/models":
			fmt.Fprint(w, `{"models":[`+
				`{"id":"other-grounder","kind":"grounding","instances":[{"actual":"running"}]},`+
				`{"id":"omniparser","kind":"grounding","instances":[{"actual":"stopped"}]},`+
				`{"id":"qwen-test","kind":"chat","instances":[{"actual":"running"}]}]}`)
		case strings.HasSuffix(r.URL.Path, "/v1/ground"):
			mu.Lock()
			*gotPath = r.URL.Path
			mu.Unlock()
			fmt.Fprint(w, `{"model":"other-grounder","width":10,"height":10,"count":1,`+
				`"elements":[{"type":"icon","interactivity":true,"content":"X","bbox":[1,1,2,2],"bbox_norm":[0.1,0.1,0.2,0.2],"center":[1,1]}],`+
				`"elapsed_ms":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestGroundingAutoPick is the F2 acceptance: with nothing configured,
// grounding resolves a running grounding model from the catalog rather than the
// hardcoded omniparser.
func TestGroundingAutoPick(t *testing.T) {
	var gotPath string
	srv := newGroundingServer(t, &gotPath)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	flagCuaImage = writeTestImage(t)
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaGround(cuaGroundCmd, nil); err != nil {
			t.Fatalf("runCuaGround: %v", err)
		}
	})
	if !strings.Contains(gotPath, "other-grounder") {
		t.Fatalf("ground path = %q, want the auto-picked running grounding model", gotPath)
	}
	_ = out
}

// TestGroundingExplicitBeatsAutoPick proves --model still wins over the
// catalog auto-pick.
func TestGroundingExplicitBeatsAutoPick(t *testing.T) {
	var gotPath string
	srv := newGroundingServer(t, &gotPath)
	defer srv.Close()
	cuaSetup(t, srv.URL)
	flagCuaImage = writeTestImage(t)
	flagCuaModel = "omniparser"
	flagJSON = true

	captureStdout(t, func() {
		if err := runCuaGround(cuaGroundCmd, nil); err != nil {
			t.Fatalf("runCuaGround: %v", err)
		}
	})
	if !strings.Contains(gotPath, "/model/omniparser/") {
		t.Fatalf("ground path = %q, want the explicit --model omniparser", gotPath)
	}
}

// TestCuaFlagSurface locks in the flag shape from the CUA fixes: --planner
// is gone from ground, --keep-image exists on every capturing command, and the
// help text does not duplicate pflag's "(default true)".
func TestCuaFlagSurface(t *testing.T) {
	if cuaGroundCmd.Flags().Lookup("planner") != nil {
		t.Error("cua ground must not define the no-op --planner flag")
	}
	for _, c := range []*cobra.Command{cuaGroundCmd, cuaClickCmd, cuaParseCmd} {
		if c.Flags().Lookup("keep-image") == nil {
			t.Errorf("%s is missing --keep-image", c.CommandPath())
		}
	}
	for _, c := range []*cobra.Command{cuaModelsCmd, cuaProbeCmd, cuaStatusCmd} {
		if c.Flags().Lookup("model") == nil {
			t.Errorf("%s is missing --model", c.CommandPath())
		}
		if c.Flags().Lookup("planner") == nil {
			t.Errorf("%s is missing --planner", c.CommandPath())
		}
	}
	// pflag appends "(default true)" for a true-defaulted bool; the usage string
	// must not carry it manually, or it renders twice.
	for _, c := range []*cobra.Command{cuaGroundCmd, cuaClickCmd, cuaParseCmd} {
		usage := c.Flags().FlagUsages()
		if n := strings.Count(usage, "(default true)"); n != 1 {
			t.Errorf("%s keep-image usage has %d occurrences of (default true):\n%s",
				c.CommandPath(), n, usage)
		}
		// Only the keep-image line should mention "default true".
		if strings.Contains(c.Flags().Lookup("keep-image").Usage, "default true") {
			t.Errorf("%s keep-image help text duplicates the default: %q",
				c.CommandPath(), c.Flags().Lookup("keep-image").Usage)
		}
	}
}
