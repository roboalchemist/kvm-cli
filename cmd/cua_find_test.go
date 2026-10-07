package cmd

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/elements"
	"github.com/roboalchemist/kvm-cli/pkg/output"
)

// saveCuaFindGlobals snapshots the selector/wait/map flag globals.
func saveCuaFindGlobals(t *testing.T) {
	t.Helper()
	text, exact, re := flagSelText, flagSelExact, flagSelRegex
	region, index, nearest := flagSelRegion, flagSelIndex, flagSelNearest
	id := flagSelID
	interactive, from, all := flagSelInteractive, flagSelFrom, flagSelAll
	mapRegion, mapScale := flagMapRegion, flagMapScale
	gone, maxWait, interval := flagWaitGone, flagWaitMaxWait, flagWaitInterval
	t.Cleanup(func() {
		flagSelText, flagSelExact, flagSelRegex = text, exact, re
		flagSelRegion, flagSelIndex, flagSelNearest = region, index, nearest
		flagSelID = id
		flagSelInteractive, flagSelFrom, flagSelAll = interactive, from, all
		flagMapRegion, flagMapScale = mapRegion, mapScale
		flagWaitGone, flagWaitMaxWait, flagWaitInterval = gone, maxWait, interval
	})
}

// cuaFindSetup prepares a hermetic run against the mock models server with a test
// image, resetting all selector-related flags.
func cuaFindSetup(t *testing.T, srvURL string) string {
	t.Helper()
	cuaSetup(t, srvURL) // resets cua + global flags, isolates HOME/env
	saveCuaFindGlobals(t)
	flagSelText, flagSelExact, flagSelRegex = "", false, ""
	flagSelRegion, flagSelIndex, flagSelNearest = "", -1, ""
	flagSelID = -1
	flagSelInteractive, flagSelFrom, flagSelAll = false, "", false
	flagMapRegion, flagMapScale = "", 1
	flagWaitGone, flagWaitMaxWait, flagWaitInterval = false, time.Second, 10*time.Millisecond
	img := writeTestImage(t)
	flagCuaImage = img
	return img
}

func TestCuaFindText(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "Settings"
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaFind(cuaFindCmd, nil); err != nil {
			t.Fatalf("runCuaFind: %v", err)
		}
	})
	var got cuaFindOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.MatchCount != 1 || got.Best == nil || got.Best.ID != 0 {
		t.Fatalf("match_count=%d best=%+v, want 1 / id 0", got.MatchCount, got.Best)
	}
	if got.ClickX != 15 || got.ClickY != 25 {
		t.Errorf("click = %v,%v want 15,25", got.ClickX, got.ClickY)
	}
}

func TestCuaFindAllNearest(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "e" // matches both "Settings" and "Hello & welcome"? only content contains 'e'
	flagSelAll = true
	flagSelNearest = "100,100"
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaFind(cuaFindCmd, nil); err != nil {
			t.Fatalf("runCuaFind: %v", err)
		}
	})
	var got cuaFindOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(got.Matches) < 2 {
		t.Fatalf("expected >=2 matches, got %d", len(got.Matches))
	}
	// Nearest first: element 1 (35,10) is closer to (100,100) than element 0 (15,25).
	if got.Matches[0].ID != 1 {
		t.Errorf("nearest order first id = %d, want 1", got.Matches[0].ID)
	}
	if got.Matches[0].Distance == 0 {
		t.Error("expected a non-zero distance")
	}
}

func TestCuaFindIndexAlone(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelIndex = 1
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaFind(cuaFindCmd, nil); err != nil {
			t.Fatalf("runCuaFind: %v", err)
		}
	})
	var got cuaFindOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Best == nil || got.Best.ID != 1 {
		t.Fatalf("best = %+v, want id 1", got.Best)
	}
}

func TestCuaFindNoMatch(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "zzz-nope"

	var runErr error
	_ = captureStdout(t, func() { runErr = runCuaFind(cuaFindCmd, nil) })
	if output.ErrorCode(runErr) != "NO_MATCH" {
		t.Fatalf("error code = %q, want NO_MATCH (%v)", output.ErrorCode(runErr), runErr)
	}
}

func TestCuaFindRegionFilter(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "Settings"
	// Region far from element 0's center (15,25) -> no match.
	flagSelRegion = "500,500,600,600"
	var runErr error
	_ = captureStdout(t, func() { runErr = runCuaFind(cuaFindCmd, nil) })
	if output.ErrorCode(runErr) != "NO_MATCH" {
		t.Fatalf("out-of-region: code = %q, want NO_MATCH", output.ErrorCode(runErr))
	}
	// Region containing element 0 -> one match.
	flagSelRegion = "0,0,50,50"
	out := captureStdout(t, func() {
		if err := runCuaFind(cuaFindCmd, nil); err != nil {
			t.Fatalf("runCuaFind: %v", err)
		}
	})
	if !bytes.Contains([]byte(out), []byte("Settings")) {
		t.Errorf("expected Settings in output: %s", out)
	}
}

func TestCuaFindFrom(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	// Produce a ground JSON first.
	flagJSON = true
	groundOut := captureStdout(t, func() {
		if err := runCuaGround(cuaGroundCmd, nil); err != nil {
			t.Fatalf("runCuaGround: %v", err)
		}
	})
	path := filepath.Join(t.TempDir(), "g.json")
	if err := os.WriteFile(path, []byte(groundOut), 0o644); err != nil {
		t.Fatal(err)
	}
	flagCuaImage = ""
	flagSelFrom = path
	flagSelText = "Hello"
	out := captureStdout(t, func() {
		if err := runCuaFind(cuaFindCmd, nil); err != nil {
			t.Fatalf("runCuaFind --from: %v", err)
		}
	})
	var got cuaFindOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Best == nil || got.Best.Content != "Hello & welcome" {
		t.Fatalf("from best = %+v", got.Best)
	}
}

func TestCuaClickSelectorDry(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "Settings"
	flagJSON = true

	out := captureStdout(t, func() {
		if err := runCuaClick(cuaClickCmd, nil); err != nil {
			t.Fatalf("runCuaClick: %v", err)
		}
	})
	var got cuaClickOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Selector == nil {
		t.Fatal("expected selector metadata in selector mode")
	}
	if got.Executed {
		t.Error("must not execute without --execute")
	}
	if got.ElementID != 0 || got.ClickX != 15 || got.ClickY != 25 {
		t.Errorf("resolved = id %d (%v,%v)", got.ElementID, got.ClickX, got.ClickY)
	}
}

func TestCuaClickSelectorExecuteNeedsYes(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "Settings"
	flagCuaExecute = true
	flagCuaYes = false

	err := runCuaClick(cuaClickCmd, nil)
	if output.ErrorCode(err) != "CONFIRMATION_REQUIRED" {
		t.Fatalf("code = %q, want CONFIRMATION_REQUIRED (%v)", output.ErrorCode(err), err)
	}
}

func TestCuaClickInstructionAndSelectorConflict(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "Settings"
	err := runCuaClick(cuaClickCmd, []string{"click settings"})
	if output.ErrorCode(err) != "USAGE" {
		t.Fatalf("code = %q, want USAGE (%v)", output.ErrorCode(err), err)
	}
}

func TestCuaClickPlannerStillWorks(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagJSON = true
	// No selector: planner path. The mock planner returns "0".
	out := captureStdout(t, func() {
		if err := runCuaClick(cuaClickCmd, []string{"click the Settings icon"}); err != nil {
			t.Fatalf("planner click: %v", err)
		}
	})
	var got cuaClickOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Selector != nil {
		t.Error("planner mode should not set selector metadata")
	}
	if got.Instruction == "" || got.ElementID != 0 {
		t.Errorf("planner result = %+v", got)
	}
}

func TestCuaWaitFound(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "Settings"
	flagWaitMaxWait = 2 * time.Second
	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaWait(cuaWaitCmd, nil); err != nil {
			t.Fatalf("wait: %v", err)
		}
	})
	var got cuaWaitOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !got.Found || got.Element == nil || got.Element.ID != 0 {
		t.Fatalf("wait result = %+v", got)
	}
}

func TestCuaWaitGone(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "not-present"
	flagWaitGone = true
	flagWaitMaxWait = 2 * time.Second
	if err := runCuaWait(cuaWaitCmd, nil); err != nil {
		t.Fatalf("wait --gone: %v", err)
	}
}

func TestCuaWaitTimeout(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelText = "not-present"
	flagWaitMaxWait = 5 * time.Millisecond
	flagWaitInterval = time.Millisecond
	var runErr error
	_ = captureStdout(t, func() { runErr = runCuaWait(cuaWaitCmd, nil) })
	if output.ErrorCode(runErr) != "TIMEOUT" {
		t.Fatalf("code = %q, want TIMEOUT (%v)", output.ErrorCode(runErr), runErr)
	}
}

func TestCuaWaitRequiresContent(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelInteractive = true // a selector, but not a content one
	err := runCuaWait(cuaWaitCmd, nil)
	if output.ErrorCode(err) != "USAGE" {
		t.Fatalf("code = %q, want USAGE (%v)", output.ErrorCode(err), err)
	}
}

func TestCuaBuildQueryValidation(t *testing.T) {
	saveCuaFindGlobals(t)
	flagSelText, flagSelRegex = "a", "b"
	if _, err := cuaBuildQuery(); output.ErrorCode(err) != "USAGE" {
		t.Errorf("text+regex: code = %q", output.ErrorCode(err))
	}
	flagSelText, flagSelRegex, flagSelExact = "", "", true
	if _, err := cuaBuildQuery(); output.ErrorCode(err) != "USAGE" {
		t.Errorf("exact without text: code = %q", output.ErrorCode(err))
	}
	flagSelExact = false
	flagSelRegion = "bad"
	if _, err := cuaBuildQuery(); output.ErrorCode(err) != "USAGE" {
		t.Errorf("bad region: code = %q", output.ErrorCode(err))
	}
	flagSelRegion = ""
	flagSelNearest = "1,2,3"
	if _, err := cuaBuildQuery(); output.ErrorCode(err) != "USAGE" {
		t.Errorf("bad nearest: code = %q", output.ErrorCode(err))
	}
}

func TestGroundRegionFilter(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelRegion = "0,20,20,40" // contains only element 0 (center 15,25), not element 1 (35,10)
	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaGround(cuaGroundCmd, nil); err != nil {
			t.Fatalf("ground: %v", err)
		}
	})
	var got cuaGroundOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Count != 1 || len(got.Elements) != 1 || got.Elements[0].Content != "Settings" {
		t.Fatalf("filtered ground = count %d elements %+v", got.Count, got.Elements)
	}
}

func TestMapTransform(t *testing.T) {
	saveCuaFindGlobals(t)
	flagMapRegion = ""
	if tr, err := cuaMapTransform(); err != nil || tr != nil {
		t.Fatalf("no map: tr=%v err=%v", tr, err)
	}
	flagMapRegion = "bad"
	if _, err := cuaMapTransform(); output.ErrorCode(err) != "USAGE" {
		t.Errorf("bad map: code = %q", output.ErrorCode(err))
	}
	flagMapRegion = "10,20,110,120"
	flagMapScale = 2
	tr, err := cuaMapTransform()
	if err != nil || tr == nil || tr.Region.X1 != 10 || tr.Scale != 2 {
		t.Fatalf("map transform = %+v err %v", tr, err)
	}
}

func TestScreenshotBadRegion(t *testing.T) {
	saveGlobals(t)
	saveCuaFindGlobals(t)
	flagShotRegion = "nope"
	err := runScreenshot(screenshotCmd, nil)
	if output.ErrorCode(err) != "USAGE" {
		t.Fatalf("code = %q, want USAGE (%v)", output.ErrorCode(err), err)
	}
	flagShotRegion = ""
}

func TestCuaText(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaText(cuaTextCmd, nil); err != nil {
			t.Fatalf("runCuaText: %v", err)
		}
	})
	var got cuaTextOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Count != 2 || len(got.Elements) != 2 {
		t.Fatalf("text = count %d elements %d, want 2/2", got.Count, len(got.Elements))
	}
}

func TestCuaTextRegionFilter(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelRegion = "0,20,20,40"
	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaText(cuaTextCmd, nil); err != nil {
			t.Fatalf("runCuaText: %v", err)
		}
	})
	var got cuaTextOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Count != 1 || got.Elements[0].Content != "Settings" {
		t.Fatalf("filtered text = %+v", got.Elements)
	}
}

func TestCuaFindID(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelID = 1
	flagJSON = true
	out := captureStdout(t, func() {
		if err := runCuaFind(cuaFindCmd, nil); err != nil {
			t.Fatalf("runCuaFind --id: %v", err)
		}
	})
	var got cuaFindOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Best == nil || got.Best.ID != 1 || got.Best.Content != "Hello & welcome" {
		t.Fatalf("--id best = %+v", got.Best)
	}
}

func TestCuaFindIDIndexConflict(t *testing.T) {
	srv := cuaModelsServer(t)
	defer srv.Close()
	cuaFindSetup(t, srv.URL)
	flagSelID = 1
	flagSelIndex = 0
	if _, err := cuaBuildQuery(); output.ErrorCode(err) != "USAGE" {
		t.Fatalf("--id + --index: code = %q, want USAGE", output.ErrorCode(err))
	}
}

func TestShotApplyCrop(t *testing.T) {
	saveCuaFindGlobals(t)
	// Build a 100x80 JPEG.
	img := image.NewRGBA(image.Rect(0, 0, 100, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	fr := shotFrame{Data: buf.Bytes(), Width: 100, Height: 80}

	// No region/scale -> unchanged.
	got, rm, sc, err := shotApplyCrop(shotOptions{}, fr)
	if err != nil || rm != nil || sc != 0 || got.Width != 100 {
		t.Fatalf("no-op crop: rm=%v sc=%v err=%v", rm, sc, err)
	}

	// Region 10,10,60,50 scaled 2x.
	r := elements.Region{X1: 10, Y1: 10, X2: 60, Y2: 50}
	got, rm, sc, err = shotApplyCrop(shotOptions{Region: &r, Scale: 2}, fr)
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	if got.Width != 100 || got.Height != 80 {
		t.Errorf("crop dims = %dx%d, want 100x80", got.Width, got.Height)
	}
	if sc != 2 || rm == nil || rm.X1 != 10 || rm.Y2 != 50 {
		t.Errorf("meta = %+v scale %v", rm, sc)
	}
}
