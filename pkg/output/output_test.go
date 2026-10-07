package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout to a pipe for the duration of fn and
// returns everything written to it.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fnErr := fn()
	_ = w.Close()
	os.Stdout = old

	data, _ := io.ReadAll(r)
	_ = r.Close()

	if fnErr != nil {
		t.Fatalf("render error: %v", fnErr)
	}
	return string(data)
}

// captureStderr redirects os.Stderr to a pipe for the duration of fn and
// returns everything written to it.
func captureStderr(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fnErr := fn()
	_ = w.Close()
	os.Stderr = old

	data, _ := io.ReadAll(r)
	_ = r.Close()

	if fnErr != nil {
		t.Fatalf("render error: %v", fnErr)
	}
	return string(data)
}

type sample struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func sampleTable() TableData {
	return TableData{
		Headers: []string{"ID", "NAME", "STATUS"},
		Rows: [][]string{
			{"1", "alpha", "on"},
			{"2", "beta", "off"},
		},
		Footer: "2 rows",
	}
}

// --- dispatch --------------------------------------------------------------

func TestRenderDispatchesJSON(t *testing.T) {
	out := captureStdout(t, func() error {
		return Render(sampleTable(), sample{ID: 1, Name: "alpha", Status: "on"}, Options{Mode: ModeJSON})
	})
	var got sample
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if got.Name != "alpha" {
		t.Errorf("got name %q, want alpha", got.Name)
	}
}

func TestRenderDispatchesYAML(t *testing.T) {
	out := captureStdout(t, func() error {
		return Render(sampleTable(), map[string]any{"name": "kvm-cli"}, Options{Mode: ModeYAML})
	})
	if !strings.Contains(out, "name: kvm-cli") {
		t.Errorf("yaml output missing name, got %q", out)
	}
}

func TestRenderDispatchesPlaintext(t *testing.T) {
	out := captureStdout(t, func() error {
		return Render(sampleTable(), nil, Options{Mode: ModePlaintext})
	})
	if out != "1\talpha\ton\n2\tbeta\toff\n" {
		t.Errorf("plaintext mismatch: got %q", out)
	}
}

func TestRenderDispatchesTable(t *testing.T) {
	out := captureStdout(t, func() error {
		return Render(sampleTable(), nil, Options{Mode: ModeTable, NoColor: true})
	})
	for _, want := range []string{"ID", "NAME", "STATUS", "alpha", "beta", "2 rows"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q, got:\n%s", want, out)
		}
	}
}

func TestRenderTableAlias(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderTable(sampleTable(), nil, Options{Mode: ModePlaintext})
	})
	if out != "1\talpha\ton\n2\tbeta\toff\n" {
		t.Errorf("RenderTable alias mismatch: got %q", out)
	}
}

// --- JSON ------------------------------------------------------------------

func TestRenderJSONPretty(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON(map[string]any{"name": "kvm-cli", "count": 2}, Options{})
	})
	if !strings.Contains(out, "\n  \"count\": 2") && !strings.Contains(out, "\n  \"count\":2") {
		t.Errorf("expected pretty-printed JSON, got %q", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["name"] != "kvm-cli" {
		t.Errorf("got %v, want kvm-cli", got["name"])
	}
}

func TestRenderJSONArray(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON([]sample{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}, Options{})
	})
	var got []sample
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got) != 2 || got[1].Name != "b" {
		t.Errorf("unexpected array: %+v", got)
	}
}

func TestRenderJSONEmptyData(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON(nil, Options{})
	})
	if strings.TrimSpace(out) != "null" {
		t.Errorf("nil renders as %q, want null", out)
	}
}

func TestRenderJSONEmptySlice(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON([]sample{}, Options{})
	})
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty slice renders as %q, want []", out)
	}
}

// --- YAML ------------------------------------------------------------------

func TestRenderYAML(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderYAML(map[string]any{"model": "RM1PE", "ok": true}, Options{})
	})
	if !strings.Contains(out, "model: RM1PE") || !strings.Contains(out, "ok: true") {
		t.Errorf("unexpected yaml: %q", out)
	}
}

func TestRenderYAMLEmpty(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderYAML(nil, Options{})
	})
	// yaml.v3 encodes nil as "null\n"
	if strings.TrimSpace(out) != "null" {
		t.Errorf("nil yaml = %q, want null", out)
	}
}

// --- plaintext -------------------------------------------------------------

func TestRenderPlaintextOmitsHeaders(t *testing.T) {
	td := TableData{
		Headers: []string{"SECRET_HEADER"},
		Rows:    [][]string{{"a", "b"}, {"c", "d"}},
	}
	out := captureStdout(t, func() error {
		return Render(td, nil, Options{Mode: ModePlaintext})
	})
	if strings.Contains(out, "SECRET_HEADER") {
		t.Errorf("plaintext must not print headers, got %q", out)
	}
	if out != "a\tb\nc\td\n" {
		t.Errorf("unexpected plaintext %q", out)
	}
}

func TestRenderPlaintextEmpty(t *testing.T) {
	out := captureStdout(t, func() error {
		return Render(TableData{Headers: []string{"A"}, Rows: nil}, nil, Options{Mode: ModePlaintext})
	})
	if out != "" {
		t.Errorf("expected empty plaintext, got %q", out)
	}
}

// --- table -----------------------------------------------------------------

func TestRenderTableEmptyRows(t *testing.T) {
	out := captureStdout(t, func() error {
		return Render(TableData{Headers: []string{"A", "B"}}, nil, Options{Mode: ModeTable, NoColor: true})
	})
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Errorf("expected headers for empty table, got %q", out)
	}
}

func TestRenderTableNoColor(t *testing.T) {
	out := captureStdout(t, func() error {
		return Render(sampleTable(), nil, Options{Mode: ModeTable, NoColor: true})
	})
	if strings.Contains(out, "\x1b[") {
		t.Errorf("NoColor output contained ANSI escapes: %q", out)
	}
}

func TestRenderTablePipedHasNoColor(t *testing.T) {
	// stdout is a pipe in tests, so shouldColor must be false regardless.
	out := captureStdout(t, func() error {
		return Render(sampleTable(), nil, Options{Mode: ModeTable})
	})
	if strings.Contains(out, "\x1b[") {
		t.Errorf("piped table output contained ANSI escapes: %q", out)
	}
}

func TestShouldColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if shouldColor(Options{}) {
		t.Error("NO_COLOR=1 should disable color")
	}
	if shouldColor(Options{NoColor: true}) {
		t.Error("NoColor flag should disable color")
	}
}

// --- fields ----------------------------------------------------------------

func TestProjectFieldsObjectPreservesOrder(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON(sample{ID: 7, Name: "alpha", Status: "on"},
			Options{Mode: ModeJSON, Fields: []string{"status", "id"}})
	})
	si := strings.Index(out, "\"status\"")
	ii := strings.Index(out, "\"id\"")
	if si == -1 || ii == -1 {
		t.Fatalf("missing projected fields in %q", out)
	}
	if si > ii {
		t.Errorf("field order not preserved (status at %d, id at %d): %q", si, ii, out)
	}
	if strings.Contains(out, "\"name\"") {
		t.Errorf("projected output should omit name: %q", out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := got["name"]; ok {
		t.Errorf("unexpected name key: %v", got)
	}
}

func TestProjectFieldsArrayOfObjects(t *testing.T) {
	data := []sample{
		{ID: 1, Name: "alpha", Status: "on"},
		{ID: 2, Name: "beta", Status: "off"},
	}
	out := captureStdout(t, func() error {
		return RenderJSON(data, Options{Mode: ModeJSON, Fields: []string{"id", "name"}})
	})
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d objects, want 2", len(got))
	}
	for _, obj := range got {
		if len(obj) != 2 {
			t.Errorf("expected 2 keys, got %v", obj)
		}
		if _, ok := obj["status"]; ok {
			t.Errorf("status should be omitted: %v", obj)
		}
		if _, ok := obj["id"]; !ok {
			t.Errorf("id missing: %v", obj)
		}
	}
}

func TestProjectFieldsUnknownOmitted(t *testing.T) {
	got := ProjectFields(sample{ID: 1}, []string{"nope"}).(*orderedObject)
	if len(got.keys) != 0 {
		t.Errorf("expected no keys, got %v", got.keys)
	}
}

func TestProjectFieldsScalarUnchanged(t *testing.T) {
	if ProjectFields("hello", []string{"x"}) != "hello" {
		t.Error("scalar should be unchanged by projection")
	}
}

func TestProjectFieldsBlankIgnored(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON(sample{ID: 5, Name: "x"}, Options{Mode: ModeJSON, Fields: []string{" ", "", "id"}})
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got) != 1 || got["id"] != float64(5) {
		t.Errorf("unexpected projection: %v", got)
	}
}

// --- jq --------------------------------------------------------------------

func TestRenderJSONJQSelect(t *testing.T) {
	data := []sample{{ID: 1, Status: "on"}, {ID: 2, Status: "off"}, {ID: 3, Status: "on"}}
	out := captureStdout(t, func() error {
		return RenderJSON(data, Options{Mode: ModeJSON, JQ: `.[] | select(.status == "on") | .id`})
	})
	want := "1\n3\n"
	if out != want {
		t.Errorf("jq select got %q, want %q", out, want)
	}
}

func TestRenderJSONJQMap(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON(map[string]any{"a": 1, "b": 2}, Options{Mode: ModeJSON, JQ: `.a + .b`})
	})
	if strings.TrimSpace(out) != "3" {
		t.Errorf("jq arithmetic got %q, want 3", out)
	}
}

func TestRenderJSONJQThenFieldsOrder(t *testing.T) {
	// Fields projection is applied before the jq expression.
	data := []sample{{ID: 1, Name: "alpha", Status: "on"}, {ID: 2, Name: "beta", Status: "off"}}
	out := captureStdout(t, func() error {
		return RenderJSON(data, Options{
			Mode:   ModeJSON,
			Fields: []string{"id", "status"},
			JQ:     `.[] | select(.status == "on") | .id`,
		})
	})
	if strings.TrimSpace(out) != "1" {
		t.Errorf("fields+jq got %q, want 1", out)
	}
}

func TestRenderJSONJQEmpty(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON([]int{1, 2, 3}, Options{Mode: ModeJSON, JQ: `empty`})
	})
	if out != "" {
		t.Errorf("jq empty should produce no output, got %q", out)
	}
}

func TestRenderJSONJQInvalidExpression(t *testing.T) {
	err := RenderJSON(map[string]any{"a": 1}, Options{Mode: ModeJSON, JQ: `.[`})
	if err == nil {
		t.Fatal("expected error for invalid jq expression")
	}
	if !strings.Contains(err.Error(), "jq") {
		t.Errorf("error should mention jq: %v", err)
	}
}

func TestRenderJSONJQRuntimeError(t *testing.T) {
	err := RenderJSON(map[string]any{}, Options{Mode: ModeJSON, JQ: `.[0]`})
	if err == nil {
		t.Fatal("expected runtime error indexing an object")
	}
}

// TestRenderJSONJQParseErrorIsUsage is the regression: a malformed jq
// expression is a usage error (exit 2), not a generic error (exit 1).
func TestRenderJSONJQParseErrorIsUsage(t *testing.T) {
	err := RenderJSON(map[string]any{"a": 1}, Options{Mode: ModeJSON, JQ: "bad("})
	if err == nil {
		t.Fatal("expected error for invalid jq expression")
	}
	if got := ErrorCode(err); got != "USAGE" {
		t.Fatalf("invalid jq error code = %q, want USAGE (err: %v)", got, err)
	}
	if !strings.Contains(err.Error(), "jq") {
		t.Fatalf("error should mention jq: %v", err)
	}
	// A runtime jq error (valid syntax) must stay a non-usage error.
	runtimeErr := RenderJSON(map[string]any{}, Options{Mode: ModeJSON, JQ: `.[0]`})
	if got := ErrorCode(runtimeErr); got == "USAGE" {
		t.Fatalf("runtime jq error should not be USAGE: %v", runtimeErr)
	}
}

// --- dotted fields () --------------------------------------------------

func TestRenderJSONFieldsDottedPath(t *testing.T) {
	data := map[string]any{
		"system": map[string]any{
			"kvmd":   map[string]any{"version": "3.1", "branch": "master"},
			"kernel": map[string]any{"release": "5.10"},
		},
		"health": 42,
	}
	out := captureStdout(t, func() error {
		return RenderJSON(data, Options{Mode: ModeJSON, Fields: []string{"system.kvmd.version"}})
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	sys, ok := got["system"].(map[string]any)
	if !ok {
		t.Fatalf("system not an object: %v", got)
	}
	kvmd, ok := sys["kvmd"].(map[string]any)
	if !ok {
		t.Fatalf("kvmd not an object: %v", sys)
	}
	if kvmd["version"] != "3.1" {
		t.Fatalf("version = %v, want 3.1", kvmd["version"])
	}
	if _, ok := kvmd["branch"]; ok {
		t.Fatalf("branch should be omitted: %v", kvmd)
	}
	if _, ok := sys["kernel"]; ok {
		t.Fatalf("kernel should be omitted: %v", sys)
	}
	if _, ok := got["health"]; ok {
		t.Fatalf("health should be omitted: %v", got)
	}
}

func TestRenderJSONFieldsDottedMergePreservesOrder(t *testing.T) {
	data := map[string]any{
		"system": map[string]any{
			"kvmd": map[string]any{"version": "3.1", "branch": "master", "extra": 1},
		},
	}
	out := captureStdout(t, func() error {
		return RenderJSON(data, Options{
			Mode:   ModeJSON,
			Fields: []string{"system.kvmd.version", "system.kvmd.branch"},
		})
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	kvmd := got["system"].(map[string]any)["kvmd"].(map[string]any)
	if kvmd["version"] != "3.1" || kvmd["branch"] != "master" {
		t.Fatalf("merged projection = %v", kvmd)
	}
	if _, ok := kvmd["extra"]; ok {
		t.Fatalf("extra should be omitted: %v", kvmd)
	}
	if strings.Index(out, `"version"`) > strings.Index(out, `"branch"`) {
		t.Fatalf("field order not preserved: %s", out)
	}
}

func TestRenderJSONFieldsDottedArray(t *testing.T) {
	data := []map[string]any{
		{"name": "a", "meta": map[string]any{"id": 1, "x": "y"}},
		{"name": "b", "meta": map[string]any{"id": 2, "x": "z"}},
	}
	out := captureStdout(t, func() error {
		return RenderJSON(data, Options{Mode: ModeJSON, Fields: []string{"meta.id"}})
	})
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	for i, obj := range got {
		meta, ok := obj["meta"].(map[string]any)
		if !ok {
			t.Fatalf("item %d meta not an object: %v", i, obj)
		}
		if _, ok := meta["id"]; !ok {
			t.Fatalf("item %d missing meta.id: %v", i, obj)
		}
		if _, ok := meta["x"]; ok {
			t.Fatalf("item %d leaked meta.x: %v", i, obj)
		}
		if _, ok := obj["name"]; ok {
			t.Fatalf("item %d leaked name: %v", i, obj)
		}
	}
}

func TestRenderJSONFieldsDottedMissingOmitted(t *testing.T) {
	data := map[string]any{
		"system": map[string]any{"kvmd": map[string]any{"version": "3.1"}},
		"ok":     true,
	}
	out := captureStdout(t, func() error {
		return RenderJSON(data, Options{Mode: ModeJSON, Fields: []string{"system.nope.deep", "ok"}})
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if _, ok := got["system"]; ok {
		t.Fatalf("system should be omitted: %v", got)
	}
	if got["ok"] != true {
		t.Fatalf("ok = %v, want true", got["ok"])
	}
}

// TestProjectFieldsFlatSingleSegmentStillWorks guards backward compatibility:
// plain multi-field projection is unchanged by dotted-path support.
func TestProjectFieldsFlatSingleSegmentStillWorks(t *testing.T) {
	got := ProjectFields(sample{ID: 7, Name: "alpha", Status: "on"}, []string{"id", "status"})
	obj, ok := got.(*orderedObject)
	if !ok {
		t.Fatalf("expected *orderedObject, got %T", got)
	}
	if len(obj.keys) != 2 || obj.keys[0] != "id" || obj.keys[1] != "status" {
		t.Fatalf("keys = %v, want [id status]", obj.keys)
	}
}

// --- errors ----------------------------------------------------------------

func TestRenderErrorJSONEnvelope(t *testing.T) {
	out := captureStderr(t, func() error {
		return RenderError(errors.New("404 not found"), Options{Mode: ModeJSON})
	})
	var env StructuredError
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid error JSON %q: %v", out, err)
	}
	if env.Error.Code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", env.Error.Code)
	}
	if env.Error.Message != "404 not found" {
		t.Errorf("message = %q", env.Error.Message)
	}
}

func TestRenderErrorPlaintext(t *testing.T) {
	out := captureStderr(t, func() error {
		return RenderError(errors.New("boom"), Options{Mode: ModePlaintext})
	})
	if !strings.Contains(out, "Error: boom") {
		t.Errorf("plaintext error = %q", out)
	}
}

func TestRenderErrorNil(t *testing.T) {
	if err := RenderError(nil, Options{Mode: ModeJSON}); err != nil {
		t.Errorf("nil error should render nothing, got %v", err)
	}
}

func TestErrorCodeExplicitWins(t *testing.T) {
	err := NewCodedError("CUSTOM_CODE", "something broke")
	if got := ErrorCode(err); got != "CUSTOM_CODE" {
		t.Errorf("ErrorCode = %q, want CUSTOM_CODE", got)
	}
}

func TestErrorCodeClassify(t *testing.T) {
	cases := map[string]string{
		"401 unauthorized":       "AUTH_INVALID",
		"403 forbidden":          "FORBIDDEN",
		"resource not found":     "NOT_FOUND",
		"connection refused":     "NETWORK_ERROR",
		"invalid argument":       "USAGE",
		"totally unexpected bug": "ERROR",
	}
	for msg, want := range cases {
		if got := ErrorCode(errors.New(msg)); got != want {
			t.Errorf("ErrorCode(%q) = %q, want %q", msg, got, want)
		}
	}
}

// statusError is a minimal error that carries an HTTP status, standing in for
// *models.Error so the classification path can be tested without importing the
// models package.
type statusError struct{ status int }

func (e statusError) Error() string   { return "status error" }
func (e statusError) HTTPStatus() int { return e.status }

// TestErrorCodeHTTPStatusClassified is the F3 unit test: an error that
// exposes an HTTP status maps to the stable code (so a models-platform 5xx maps
// to DEVICE_ERROR and therefore exit 3, not exit 1).
func TestErrorCodeHTTPStatusClassified(t *testing.T) {
	cases := map[int]string{
		401: "AUTH_INVALID",
		403: "FORBIDDEN",
		404: "NOT_FOUND",
		500: "DEVICE_ERROR",
		502: "DEVICE_ERROR",
		503: "DEVICE_ERROR",
	}
	for status, want := range cases {
		if got := ErrorCode(statusError{status}); got != want {
			t.Errorf("ErrorCode(status %d) = %q, want %q", status, got, want)
		}
	}
	// A status with no specific mapping falls through to message classification.
	if got := ErrorCode(statusError{400}); got != "ERROR" {
		t.Errorf("ErrorCode(status 400) = %q, want ERROR", got)
	}
	// An explicit code still wins over the status.
	if got := ErrorCode(WrapCodedError("CUSTOM", statusError{500}, "")); got != "CUSTOM" {
		t.Errorf("explicit code = %q, want CUSTOM", got)
	}
}

// TestErrorCodeClassifyContextDeadline covers the F3 transport branch:
// context deadline/cancel errors must map to NETWORK_ERROR (exit 3).
func TestErrorCodeClassifyContextDeadline(t *testing.T) {
	for _, msg := range []string{
		`Get "https://models.example/api/models": context deadline exceeded`,
		"context canceled",
	} {
		if got := ErrorCode(errors.New(msg)); got != "NETWORK_ERROR" {
			t.Errorf("ErrorCode(%q) = %q, want NETWORK_ERROR", msg, got)
		}
	}
}

func TestWrapCodedError(t *testing.T) {
	base := errors.New("dial failed")
	wrapped := WrapCodedError("NETWORK_ERROR", base, "cannot reach device")
	if ErrorCode(wrapped) != "NETWORK_ERROR" {
		t.Errorf("code = %q", ErrorCode(wrapped))
	}
	if wrapped.Error() != "cannot reach device" {
		t.Errorf("message = %q", wrapped.Error())
	}
	if !errors.Is(wrapped, base) {
		t.Error("wrapped error should unwrap to base")
	}
}

// --- helpers ---------------------------------------------------------------

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":          ModeTable,
		"table":     ModeTable,
		"json":      ModeJSON,
		"JSON":      ModeJSON,
		"plaintext": ModePlaintext,
		"plain":     ModePlaintext,
		"yaml":      ModeYAML,
		"yml":       ModeYAML,
		"bogus":     ModeTable,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestModeString(t *testing.T) {
	cases := map[Mode]string{
		ModeTable:     "table",
		ModeJSON:      "json",
		ModePlaintext: "plaintext",
		ModeYAML:      "yaml",
		Mode(99):      "unknown",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", m, got, want)
		}
	}
}

// --- internal error paths --------------------------------------------------

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// badYAML always fails to marshal, exercising renderYAML's error path without
// triggering yaml.v3's panic on unsupported Go types.
type badYAML struct{}

func (badYAML) MarshalYAML() (any, error) { return nil, errors.New("yaml boom") }

func TestProjectFieldsNoFields(t *testing.T) {
	s := sample{ID: 1, Name: "x"}
	got, ok := ProjectFields(s, nil).(sample)
	if !ok || got != s {
		t.Errorf("ProjectFields with no fields should return input, got %#v", got)
	}
}

func TestProjectFieldsUnmarshalable(t *testing.T) {
	ch := make(chan int)
	if got := ProjectFields(ch, []string{"a"}); got != any(ch) {
		t.Errorf("unmarshalable value should be returned unchanged")
	}
}

func TestOrderedObjectMarshalError(t *testing.T) {
	o := newOrderedObject()
	o.set("bad", make(chan int))
	if _, err := o.MarshalJSON(); err == nil {
		t.Error("expected marshal error for chan value")
	}
}

func TestWriteJSONMarshalError(t *testing.T) {
	if err := writeJSON(&bytes.Buffer{}, make(chan int)); err == nil {
		t.Error("expected marshal error")
	}
}

func TestWriteJSONWriteError(t *testing.T) {
	if err := writeJSON(failWriter{}, map[string]int{"a": 1}); err == nil {
		t.Error("expected write error")
	}
}

func TestRenderPlaintextWriteError(t *testing.T) {
	if err := renderPlaintext(failWriter{}, TableData{Rows: [][]string{{"a"}}}); err == nil {
		t.Error("expected write error")
	}
}

func TestRenderYAMLMarshalError(t *testing.T) {
	if err := renderYAML(&bytes.Buffer{}, badYAML{}); err == nil {
		t.Error("expected yaml encode error")
	}
}

func TestRenderJSONJQNormalizeError(t *testing.T) {
	if err := renderJSON(&bytes.Buffer{}, make(chan int), Options{JQ: "."}); err == nil {
		t.Error("expected normalize error")
	}
}

func TestRenderTableWriteError(t *testing.T) {
	if err := renderTable(failWriter{}, sampleTable(), Options{NoColor: true}); err == nil {
		t.Error("expected table render/write error")
	}
}

func TestCodedErrorMessageBranches(t *testing.T) {
	if got := (&CodedError{Code: "X"}).Error(); got != "X" {
		t.Errorf("bare code = %q, want X", got)
	}
	base := errors.New("base msg")
	if got := (&CodedError{Code: "X", Err: base}).Error(); got != "base msg" {
		t.Errorf("wrapped = %q, want base msg", got)
	}
	if got := (&CodedError{Code: "X", Message: "explicit", Err: base}).Error(); got != "explicit" {
		t.Errorf("explicit = %q, want explicit", got)
	}
}

func TestErrorCodeNil(t *testing.T) {
	if got := ErrorCode(nil); got != "" {
		t.Errorf("ErrorCode(nil) = %q, want empty", got)
	}
}

func TestNewStructuredErrorNil(t *testing.T) {
	if got := NewStructuredError(nil).Error.Code; got != "OK" {
		t.Errorf("nil envelope code = %q, want OK", got)
	}
}

// TestStructuredErrorGuidance is the F12 acceptance: the envelope carries
// recoverable/suggestion, populated from the error code.
func TestStructuredErrorGuidance(t *testing.T) {
	cases := []struct {
		code        string
		recoverable bool
		suggestion  bool
	}{
		{"USAGE", true, true},
		{"NETWORK_ERROR", true, true},
		{"AUTH_INVALID", true, true},
		{"DEVICE_ERROR", true, true},
		{"FORBIDDEN", false, true},
		{"NOT_FOUND", false, true},
		{"ERROR", false, false},
	}
	for _, tc := range cases {
		env := NewStructuredError(NewCodedError(tc.code, "boom"))
		if env.Error.Code != tc.code {
			t.Errorf("code = %q, want %q", env.Error.Code, tc.code)
		}
		if env.Error.Recoverable != tc.recoverable {
			t.Errorf("%s: recoverable = %v, want %v", tc.code, env.Error.Recoverable, tc.recoverable)
		}
		if gotSug := env.Error.Suggestion != ""; gotSug != tc.suggestion {
			t.Errorf("%s: suggestion present = %v, want %v (%q)", tc.code, gotSug, tc.suggestion, env.Error.Suggestion)
		}
	}
}

// TestStructuredErrorJSONAdditive locks the on-wire shape: recoverable is
// always present and suggestion appears only when non-empty, while code and
// message remain unchanged for existing consumers.
func TestStructuredErrorJSONAdditive(t *testing.T) {
	out := captureStderr(t, func() error {
		return RenderError(NewCodedError("NETWORK_ERROR", "dial failed"), Options{Mode: ModeJSON})
	})
	var raw map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if raw["error"]["code"] != "NETWORK_ERROR" || raw["error"]["message"] != "dial failed" {
		t.Fatalf("envelope = %v", raw)
	}
	if raw["error"]["recoverable"] != true {
		t.Fatalf("recoverable = %v, want true", raw["error"]["recoverable"])
	}
	if s, _ := raw["error"]["suggestion"].(string); s == "" {
		t.Fatalf("suggestion missing: %v", raw["error"])
	}
}

func TestRunJQWriteError(t *testing.T) {
	if err := runJQ(failWriter{}, map[string]any{"a": 1}, "."); err == nil {
		t.Error("expected jq write error")
	}
}

// --- secret redaction () ----------------------------------------------

const testMask = "***"

// TestRenderRedactsTableSecrets is the read-path acceptance: a
// key/value table carrying a secret-named field must render the mask in the
// value column while leaving non-secret fields untouched.
func TestRenderRedactsTableSecrets(t *testing.T) {
	td := TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows: [][]string{
			{"ssl_key", "PRIVATEKEY"},
			{"config.authkey", "AUTHVALUE"},
			{"keyboard", "us-qwerty"},
			{"keymaps", "de-neo"},
			{"default_product_id", "PID-1234"},
			{"ssl_cert", "CERTPEM"},
		},
	}
	out := captureStdout(t, func() error {
		return Render(td, nil, Options{Mode: ModeTable, NoColor: true})
	})
	for _, leaked := range []string{"PRIVATEKEY", "AUTHVALUE"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("table leaked %q:\n%s", leaked, out)
		}
	}
	for _, kept := range []string{"keyboard", "us-qwerty", "keymaps", "de-neo", "default_product_id", "PID-1234", "ssl_cert", "CERTPEM"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("table dropped non-secret %q:\n%s", kept, out)
		}
	}
	if !strings.Contains(out, testMask) {
		t.Fatalf("table did not render the mask:\n%s", out)
	}
}

// TestRenderRedactsJSONSecrets checks the JSON path masks secret-named keys
// recursively while preserving benign keys.
func TestRenderRedactsJSONSecrets(t *testing.T) {
	data := map[string]any{
		"ssl_key":         "PRIVATEKEY",
		"keyboard":        "us-qwerty",
		"default_product": "PID-1234",
		"nested":          map[string]any{"wifi_password": "PW", "ssid": "keep"},
		"list":            []any{map[string]any{"authkey": "AUTHVALUE"}, "plain"},
	}
	out := captureStdout(t, func() error {
		return Render(TableData{}, data, Options{Mode: ModeJSON})
	})
	for _, leaked := range []string{"PRIVATEKEY", "AUTHVALUE", `"PW"`} {
		if strings.Contains(out, leaked) {
			t.Fatalf("json leaked %q:\n%s", leaked, out)
		}
	}
	for _, kept := range []string{"keyboard", "us-qwerty", "default_product", "PID-1234", "ssid", "keep", "plain"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("json dropped non-secret %q:\n%s", kept, out)
		}
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["ssl_key"] != testMask {
		t.Fatalf("ssl_key = %v, want %q", got["ssl_key"], testMask)
	}
}

// TestRenderRedactsYAMLSecrets covers the YAML path.
func TestRenderRedactsYAMLSecrets(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderYAML(map[string]any{"setup_key": "SETUP", "keyboard": "us"}, Options{})
	})
	if strings.Contains(out, "SETUP") {
		t.Fatalf("yaml leaked the secret:\n%s", out)
	}
	if !strings.Contains(out, "keyboard: us") {
		t.Fatalf("yaml dropped the benign field:\n%s", out)
	}
}

// TestRenderPlaintextRedacts confirms plaintext (piping) output is masked too.
func TestRenderPlaintextRedacts(t *testing.T) {
	td := TableData{Rows: [][]string{{"password", "hunter2"}, {"ssid", "keep"}}}
	out := captureStdout(t, func() error {
		return Render(td, nil, Options{Mode: ModePlaintext})
	})
	if out != "password\t"+testMask+"\nssid\tkeep\n" {
		t.Fatalf("plaintext = %q", out)
	}
}

// TestRenderJSONDirectRedacts verifies the exported RenderJSON helper also
// redacts (commands and tests call it directly).
func TestRenderJSONDirectRedacts(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON(map[string]any{"private_key": "PK"}, Options{})
	})
	if strings.Contains(out, "PK") {
		t.Fatalf("RenderJSON leaked the secret:\n%s", out)
	}
	if !strings.Contains(out, testMask) {
		t.Fatalf("RenderJSON did not mask:\n%s", out)
	}
}

// TestRenderNoRedactEscapeHatch verifies opts.NoRedact disables the pass, so a
// caller that already redacted (or renders non-device data) can opt out.
func TestRenderNoRedactEscapeHatch(t *testing.T) {
	out := captureStdout(t, func() error {
		return RenderJSON(map[string]any{"ssl_key": "PRIVATEKEY"}, Options{NoRedact: true})
	})
	if !strings.Contains(out, "PRIVATEKEY") {
		t.Fatalf("NoRedact should bypass masking, got:\n%s", out)
	}
}

// TestRenderRedactsNamedMapType verifies the reflection-based redactor handles
// named map types (the api.RawMap shape) used by the info command.
func TestRenderRedactsNamedMapType(t *testing.T) {
	type rawMap map[string]any
	out := captureStdout(t, func() error {
		return Render(TableData{}, rawMap{"auth": map[string]any{"token": "SECRET"}, "enabled": true}, Options{Mode: ModeJSON})
	})
	if strings.Contains(out, "SECRET") {
		t.Fatalf("named map type leaked the secret:\n%s", out)
	}
}

// TestErrorCodeClassifyEOF is the N3 regression: a bare EOF (or a
// connection reset) from a truncated/dropped connection must be a network
// error, not a generic user error.
func TestErrorCodeClassifyEOF(t *testing.T) {
	for _, msg := range []string{"EOF", "unexpected EOF", "read tcp: connection reset by peer", "write: broken pipe"} {
		if got := ErrorCode(errors.New(msg)); got != "NETWORK_ERROR" {
			t.Errorf("ErrorCode(%q) = %q, want NETWORK_ERROR", msg, got)
		}
	}
}
