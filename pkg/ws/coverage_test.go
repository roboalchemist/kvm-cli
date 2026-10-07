package ws

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// covFake: a configurable in-process WS + REST server. Unlike fakeKVM (which
// models the happy path), covFake lets a test choose the handshake frames, drive
// per-message replies, return arbitrary REST responses and simulate failure
// modes. Everything is hermetic; no real device is involved.
// ---------------------------------------------------------------------------

type covFake struct {
	t   *testing.T
	srv *httptest.Server

	// handshake is sent immediately after the WS upgrade. nil => default set.
	handshake []string
	// onMessage may return extra frames to send after recording an inbound one.
	onMessage func(ev map[string]any) []string
	// closeAfterHandshake drops the connection once the handshake is written.
	closeAfterHandshake bool
	// noUpgrade makes /api/ws return HTTP 500 without upgrading (dial failure).
	noUpgrade bool

	restStatus int
	restBody   string

	mu          sync.Mutex
	conn        *websocket.Conn
	events      []map[string]any
	restBodies  []string
	restQueries []map[string]string
}

func newCovFake(t *testing.T) *covFake {
	t.Helper()
	f := &covFake{t: t, restStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", f.handleWS)
	mux.HandleFunc("/api/hid/set_params", f.handleREST)
	mux.HandleFunc("/api/hid/print", f.handleREST)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *covFake) URL() string { return f.srv.URL }

func (f *covFake) handleWS(w http.ResponseWriter, r *http.Request) {
	if f.noUpgrade {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = c
	hs := f.handshake
	onMsg := f.onMessage
	f.mu.Unlock()
	defer func() { _ = c.Close() }()

	msgs := hs
	if msgs == nil {
		msgs = []string{
			`{"event_type":"loop","event":{"version":{"major":4,"minor":82}}}`,
			`{"event_type":"hid","event":{"enabled":true,"online":true,"connected":true,"keyboard":{"online":true,"leds":{"caps":false,"num":false,"scroll":false},"outputs":{"active":"","available":[]}},"mouse":{"online":true,"absolute":true,"outputs":{"active":"usb","available":["usb","usb_rel","usb_hybrid","usb_touch"]}}}}`,
			`{"event_type":"streamer","event":{"streamer":{"h264":{"online":true,"fps":60},"sinks":{"jpeg":{"has_clients":true},"h264":{"has_clients":true}},"source":{"resolution":{"width":2560,"height":1440}}}}}`,
		}
	}
	for _, m := range msgs {
		if err := c.WriteMessage(websocket.TextMessage, []byte(m)); err != nil {
			return
		}
	}
	if f.closeAfterHandshake {
		return
	}

	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var ev map[string]any
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		f.mu.Lock()
		f.events = append(f.events, ev)
		f.mu.Unlock()

		if onMsg != nil {
			for _, reply := range onMsg(ev) {
				_ = c.WriteMessage(websocket.TextMessage, []byte(reply))
			}
		} else if ev["event_type"] == "ping" {
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"event_type":"pong","event":{}}`))
		}
	}
}

func (f *covFake) handleREST(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.restBodies = append(f.restBodies, string(raw))
	q := map[string]string{}
	for k := range r.URL.Query() {
		q[k] = r.URL.Query().Get(k)
	}
	f.restQueries = append(f.restQueries, q)
	status := f.restStatus
	body := f.restBody
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
	}
	if body != "" {
		_, _ = w.Write([]byte(body))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
}

func (f *covFake) allEvents() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.events))
	copy(out, f.events)
	return out
}

func (f *covFake) eventsOf(kind string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, e := range f.events {
		if e["event_type"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func (f *covFake) waitEvents(n int) {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.events)
		f.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %d events", n)
}

func (f *covFake) restCalls() ([]string, []map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := append([]string(nil), f.restBodies...)
	q := append([]map[string]string(nil), f.restQueries...)
	return b, q
}

// connectCov dials a covFake. It does not wait for any state by default.
func connectCov(t *testing.T, f *covFake, opts Options) *Client {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	c, err := Connect(f.URL(), "testtoken", opts)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// ---------------------------------------------------------------------------
// Connection, lifecycle and low-level helpers
// ---------------------------------------------------------------------------

func TestConnectValidationErrors(t *testing.T) {
	if _, err := Connect("   ", "tok", Options{}); err == nil {
		t.Error("Connect with empty base URL should error")
	}
	if _, err := Connect("http://host", "   ", Options{}); err == nil {
		t.Error("Connect with empty token should error")
	}
	// normalizeBaseURL keeps the http:// prefix, url.Parse then fails on the
	// malformed IPv6 literal.
	if _, err := Connect("http://[::1", "tok", Options{}); err == nil {
		t.Error("Connect with unparseable base URL should error")
	}
}

func TestConnectDialFailures(t *testing.T) {
	// Server responds but refuses to upgrade -> gorilla returns a response.
	f := newCovFake(t)
	f.noUpgrade = true
	_, err := Connect(f.URL(), "tok", Options{Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("dial to non-upgrading server: err = %v, want HTTP 500", err)
	}

	// Dead endpoint -> error with no response.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if _, err := Connect(deadURL, "tok", Options{Timeout: 500 * time.Millisecond}); err == nil {
		t.Error("dial to closed server should error")
	}
}

func TestConnectTimeoutDefault(t *testing.T) {
	// Timeout <= 0 must fall back to the default; writeTimeout must too.
	f := newCovFake(t)
	c, err := Connect(f.URL(), "tok", Options{Timeout: 0})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.WaitFor(func(s State) bool { return s.HIDSeen }, 3*time.Second); err != nil {
		t.Fatalf("wait hid: %v", err)
	}
	if got := c.writeTimeout(); got != defaultTimeout {
		t.Errorf("writeTimeout with zero option = %v, want %v", got, defaultTimeout)
	}
	if got := (&Client{opts: Options{Timeout: 3 * time.Second}}).writeTimeout(); got != 3*time.Second {
		t.Errorf("writeTimeout with option = %v, want 3s", got)
	}
}

func TestClientAccessorsAndCloseIdempotent(t *testing.T) {
	f := newCovFake(t)
	c := connectCov(t, f, Options{})
	if _, err := c.WaitFor(func(s State) bool { return s.HIDSeen }, 3*time.Second); err != nil {
		t.Fatalf("wait hid: %v", err)
	}
	if c.BaseURL() == "" {
		t.Error("BaseURL returned empty")
	}
	select {
	case <-c.Done():
		t.Error("Done closed before Close")
	default:
	}
	if err := c.Err(); err != nil {
		t.Errorf("Err before close = %v, want nil", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close should be a no-op: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Error("Done not closed after Close")
	}
}

func TestReadLoopErrorSetsErr(t *testing.T) {
	// The server drops the socket right after the handshake, so readLoop sees a
	// terminal read error while done is still open and records it via setErr.
	f := newCovFake(t)
	f.closeAfterHandshake = true
	c, err := Connect(f.URL(), "tok", Options{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.Err() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Err never became non-nil after server closed the socket")
}

func TestKeepalivePingError(t *testing.T) {
	// Build a client around a live socket, close the socket, then run keepalive
	// with a very short cadence: the ping write must fail and the goroutine must
	// exit cleanly.
	f := newCovFake(t)
	wsURL, err := buildWSURL(f.URL(), "tok", true)
	if err != nil {
		t.Fatalf("buildWSURL: %v", err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	c := &Client{conn: conn, done: make(chan struct{}), pingInterval: 5 * time.Millisecond}
	c.wg.Add(1)
	go c.keepalive()

	exited := make(chan struct{})
	go func() { c.wg.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		close(c.done)
		t.Fatal("keepalive did not exit after ping failure")
	}
	close(c.done)
}

func TestWriteFrameAndJSONErrors(t *testing.T) {
	// Marshal failure (channel is not JSON-encodable).
	c := &Client{done: make(chan struct{})}
	if err := c.writeJSON("ping", make(chan int)); err == nil {
		t.Error("writeJSON with unmarshalable payload should error")
	}
	// Send after close.
	close(c.done)
	if err := c.writeFrame(websocket.TextMessage, []byte("{}")); err == nil {
		t.Error("writeFrame after close should error")
	}
	if err := c.sendPing(); err == nil {
		t.Error("sendPing after close should error")
	}
}

func TestNormalizeBaseURLAndRedact(t *testing.T) {
	if got := normalizeBaseURL(""); got != "" {
		t.Errorf("normalizeBaseURL(\"\") = %q, want empty", got)
	}
	if got := normalizeBaseURL("  https://Host/  "); got != "https://Host" {
		t.Errorf("normalizeBaseURL trimmed = %q, want https://Host", got)
	}
	if got := normalizeBaseURL("host/"); got != "https://host" {
		t.Errorf("normalizeBaseURL bare = %q, want https://host", got)
	}
	// An unparseable URL is returned verbatim.
	if got := redactURL("http://[::1"); got != "http://[::1" {
		t.Errorf("redactURL invalid = %q, want the raw input", got)
	}
}

func TestDebugfEnabled(t *testing.T) {
	// Exercise the Debug branch (output goes to stderr; we only need coverage).
	c := &Client{opts: Options{Debug: true}}
	c.debugf("coverage debug %d", 1)
}

func TestWaitForTimeoutAndClosed(t *testing.T) {
	// Timeout path.
	c := &Client{done: make(chan struct{})}
	if _, err := c.WaitFor(func(State) bool { return false }, 30*time.Millisecond); err == nil {
		t.Error("WaitFor should time out")
	}

	// Closed-while-waiting path.
	closed := &Client{done: make(chan struct{})}
	close(closed.done)
	if _, err := closed.WaitFor(func(State) bool { return false }, time.Second); err == nil {
		t.Error("WaitFor on a closed client should error")
	}

	// Resolution defaults the timeout and surfaces the wait error.
	if _, err := closed.Resolution(0); err == nil {
		t.Error("Resolution on a closed client should error")
	}
}

// ---------------------------------------------------------------------------
// Keys: validation, aliases, ordering
// ---------------------------------------------------------------------------

func TestKeyValidationAliasesAndErrors(t *testing.T) {
	aliases := map[string]string{
		"option": "AltLeft", "win": "MetaLeft", "super": "MetaLeft",
		"bksp": "Backspace", "prtsc": "PrintScreen", "grave": "Backquote",
		"dot": "Period", "equals": "Equal", "space key": "Space",
		"control": "ControlLeft", "meta": "MetaLeft", "lwin": "MetaLeft",
		"numpadenter": "NumpadEnter", "page down": "PageDown",
	}
	for in, want := range aliases {
		got, err := ResolveKey(in)
		if err != nil {
			t.Errorf("ResolveKey(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ResolveKey(%q) = %q, want %q", in, got, want)
		}
	}

	for _, bad := range []string{"", "bogus", "f25", "f0", "ctrl+alt", "not a key"} {
		if got, err := ResolveKey(bad); err == nil {
			t.Errorf("ResolveKey(%q) = %q, want error", bad, got)
		}
	}

	// parseUint is used by the fN path.
	if _, err := parseUint(""); err == nil {
		t.Error("parseUint(\"\") should error")
	}
	if _, err := parseUint("x"); err == nil {
		t.Error("parseUint(\"x\") should error")
	}
	if n, err := parseUint("12"); err != nil || n != 12 {
		t.Errorf("parseUint(\"12\") = %d, %v", n, err)
	}

	// ResolveCombo error paths.
	if _, err := ResolveCombo(nil); err == nil {
		t.Error("ResolveCombo(nil) should error")
	}
	if _, err := ResolveCombo([]string{"a", "bogus"}); err == nil {
		t.Error("ResolveCombo with unknown key should error")
	}

	// IsValidKey / ValidKeys sanity.
	if !IsValidKey("KeyA") || !IsValidKey("F24") || !IsValidKey("Numpad9") {
		t.Error("IsValidKey should accept generated codes")
	}
	if IsValidKey("keya") || IsValidKey("f25") {
		t.Error("IsValidKey should reject non-exact codes")
	}
	if len(ValidKeys()) < 100 {
		t.Error("ValidKeys returned too few codes")
	}
}

func TestComboExactFrames(t *testing.T) {
	f := newCovFake(t)
	c := connectCov(t, f, Options{})

	if err := c.Combo([]string{"ctrl", "alt", "del"}); err != nil {
		t.Fatalf("Combo: %v", err)
	}
	f.waitEvents(6)

	type frame struct {
		key           string
		state, finish bool
	}
	want := []frame{
		{"ControlLeft", true, false},
		{"AltLeft", true, false},
		{"Delete", true, false},
		{"Delete", false, false},
		{"AltLeft", false, false},
		{"ControlLeft", false, false},
	}
	keys := f.eventsOf("key")
	if len(keys) != len(want) {
		t.Fatalf("got %d key frames, want %d: %v", len(keys), len(want), keys)
	}
	for i, w := range want {
		ev := keys[i]["event"].(map[string]any)
		if ev["key"] != w.key || ev["state"] != w.state || ev["finish"] != w.finish {
			t.Errorf("frame %d = %v, want key=%s state=%v finish=%v", i, ev, w.key, w.state, w.finish)
		}
	}
}

func TestKeyAndComboErrorPaths(t *testing.T) {
	// Resolve failures happen before any I/O.
	c := &Client{}
	if err := c.PressKey("bogus"); err == nil {
		t.Error("PressKey(bogus) should error")
	}
	if err := c.ReleaseKey("bogus"); err == nil {
		t.Error("ReleaseKey(bogus) should error")
	}
	if err := c.TapKey("bogus"); err == nil {
		t.Error("TapKey(bogus) should error")
	}
	if err := c.Combo([]string{"bogus"}); err == nil {
		t.Error("Combo(bogus) should error")
	}
	if err := c.Combo(nil); err == nil {
		t.Error("Combo(nil) should error")
	}

	// Send-after-close fails inside the write loop.
	f := newCovFake(t)
	live := connectCov(t, f, Options{})
	if _, err := live.WaitFor(func(s State) bool { return s.HIDSeen }, 3*time.Second); err != nil {
		t.Fatalf("wait hid: %v", err)
	}
	_ = live.Close()
	if err := live.PressKey("a"); err == nil {
		t.Error("PressKey after Close should error")
	}
	if err := live.Combo([]string{"ctrl"}); err == nil {
		t.Error("Combo after Close should error")
	}
}

// ---------------------------------------------------------------------------
// Mouse: scaling, clamping, frames
// ---------------------------------------------------------------------------

func TestMouseScalingAndClamping(t *testing.T) {
	// Pure helpers: clamping on both ends of int16.
	if got := clampInt16(-40000); got != -32768 {
		t.Errorf("clampInt16(-40000) = %d, want -32768", got)
	}
	if got := clampInt16(40000); got != 32767 {
		t.Errorf("clampInt16(40000) = %d, want 32767", got)
	}
	if got := clampInt16(5); got != 5 {
		t.Errorf("clampInt16(5) = %d, want 5", got)
	}
	if got := clampInt8(-1000); got != -128 {
		t.Errorf("clampInt8(-1000) = %d, want -128", got)
	}
	if got := clampInt8(1000); got != 127 {
		t.Errorf("clampInt8(1000) = %d, want 127", got)
	}
	if got := clampInt8(5); got != 5 {
		t.Errorf("clampInt8(5) = %d, want 5", got)
	}
	// Negative rounding branch.
	if got := round(-1.6); got != -2 {
		t.Errorf("round(-1.6) = %v, want -2", got)
	}
	if got := round(1.6); got != 2 {
		t.Errorf("round(1.6) = %v, want 2", got)
	}
	// scalePercent clamps below zero and above 100.
	if got := scalePercent(-10); got != -32768 {
		t.Errorf("scalePercent(-10) = %d, want -32768", got)
	}
	if got := scalePercent(200); got != 32767 {
		t.Errorf("scalePercent(200) = %d, want 32767", got)
	}
	// PixelToAbs clamps out-of-range pixels.
	if x, y := PixelToAbs(-10, -10, 100, 100); x != -32768 || y != -32768 {
		t.Errorf("PixelToAbs below range = (%d,%d), want (-32768,-32768)", x, y)
	}
	if x, y := PixelToAbs(200, 200, 100, 100); x != 32767 || y != 32767 {
		t.Errorf("PixelToAbs above range = (%d,%d), want (32767,32767)", x, y)
	}
	if x, y := PixelToAbs(10, 10, 0, 0); x != 0 || y != 0 {
		t.Errorf("PixelToAbs zero dim = (%d,%d), want (0,0)", x, y)
	}
}

func TestMouseMoveFramesAndRelativeClamp(t *testing.T) {
	f := newCovFake(t)
	c := connectCov(t, f, Options{})
	if _, err := c.WaitFor(func(s State) bool { return s.HasResolution }, 3*time.Second); err != nil {
		t.Fatalf("resolution: %v", err)
	}

	if err := c.MouseMovePct(-10, -10); err != nil {
		t.Fatalf("move pct lo: %v", err)
	}
	if err := c.MouseMovePct(200, 200); err != nil {
		t.Fatalf("move pct hi: %v", err)
	}
	// Relative/wheel values are clamped to the signed 8-bit range.
	if err := c.MouseRelative(1000, -1000); err != nil {
		t.Fatalf("relative: %v", err)
	}
	if err := c.MouseWheel(-500, 500); err != nil {
		t.Fatalf("wheel: %v", err)
	}
	f.waitEvents(4)

	moves := f.eventsOf("mouse_move")
	if len(moves) != 2 {
		t.Fatalf("mouse_move frames = %d, want 2", len(moves))
	}
	lo := moves[0]["event"].(map[string]any)["to"].(map[string]any)
	if lo["x"].(float64) != -32768 || lo["y"].(float64) != -32768 {
		t.Errorf("pct(-10,-10) = %v, want clamped -32768", lo)
	}
	hi := moves[1]["event"].(map[string]any)["to"].(map[string]any)
	if hi["x"].(float64) != 32767 || hi["y"].(float64) != 32767 {
		t.Errorf("pct(200,200) = %v, want clamped 32767", hi)
	}

	rel := f.eventsOf("mouse_relative")
	if len(rel) != 1 {
		t.Fatalf("mouse_relative frames = %d, want 1", len(rel))
	}
	rd := rel[0]["event"].(map[string]any)["delta"].(map[string]any)
	if rd["x"].(float64) != 127 || rd["y"].(float64) != -128 {
		t.Errorf("relative delta = %v, want {127,-128}", rd)
	}

	wheel := f.eventsOf("mouse_wheel")
	if len(wheel) != 1 {
		t.Fatalf("mouse_wheel frames = %d, want 1", len(wheel))
	}
	wd := wheel[0]["event"].(map[string]any)["delta"].(map[string]any)
	if wd["x"].(float64) != -128 || wd["y"].(float64) != 127 {
		t.Errorf("wheel delta = %v, want {-128,127}", wd)
	}
}

func TestDeltaSquashFlag(t *testing.T) {
	// squash is omitempty: a plain relative/wheel frame must not carry it, but an
	// explicit true must serialise.
	plain, err := json.Marshal(deltaEvent{Delta: point{X: 1, Y: 2}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(plain), "squash") {
		t.Errorf("deltaEvent without squash should omit it: %s", plain)
	}
	squashed, err := json.Marshal(deltaEvent{Delta: point{X: 1, Y: 2}, Squash: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(squashed), `"squash":true`) {
		t.Errorf("deltaEvent with squash should serialise it: %s", squashed)
	}
}

func TestMouseMovePixelsResolutionError(t *testing.T) {
	c := &Client{done: make(chan struct{})}
	close(c.done)
	if err := c.MouseMovePixels(1, 1); err == nil {
		t.Error("MouseMovePixels without resolution should error")
	}
}

func TestMouseClickErrorPath(t *testing.T) {
	f := newCovFake(t)
	c := connectCov(t, f, Options{})
	if _, err := c.WaitFor(func(s State) bool { return s.HIDSeen }, 3*time.Second); err != nil {
		t.Fatalf("wait hid: %v", err)
	}
	_ = c.Close()
	if err := c.MouseClick("left"); err == nil {
		t.Error("MouseClick after Close should error")
	}
}

// ---------------------------------------------------------------------------
// State folding
// ---------------------------------------------------------------------------

func TestStateFoldingFull(t *testing.T) {
	f := newCovFake(t)
	f.handshake = []string{
		`{"event_type":"loop","event":{"version":{"major":4,"minor":82}}}`,
		`{"event_type":"info","event":{"system":{"kvmd":{"version":"1.2.3"}}}}`,
		`{"event_type":"hid","event":{"enabled":true,"online":true,"busy":false,"connected":true,"keyboard":{"online":true,"leds":{"caps":true,"num":false,"scroll":false},"outputs":{"active":"","available":[]}},"mouse":{"online":true,"absolute":true,"outputs":{"active":"usb","available":["usb","usb_rel"]}}}}`,
		`{"event_type":"streamer","event":{"streamer":{"h264":{"online":false,"fps":60},"sinks":{"jpeg":{"has_clients":true},"h264":{"has_clients":false}},"source":{"resolution":{"width":2560,"height":1440}}}}}`,
		`{"event_type":"pong","event":{}}`,
	}
	c := connectCov(t, f, Options{})

	st, err := c.WaitFor(func(s State) bool {
		return s.HIDSeen && s.StreamerSeen && !s.LastPong.IsZero()
	}, 3*time.Second)
	if err != nil {
		t.Fatalf("wait for full state: %v", err)
	}
	if st.ProtocolVersion != "4.82" {
		t.Errorf("ProtocolVersion = %q, want 4.82", st.ProtocolVersion)
	}
	if st.KVMVersion != "1.2.3" {
		t.Errorf("KVMVersion = %q, want 1.2.3", st.KVMVersion)
	}
	if !st.HID.CapsLock || !st.HID.Absolute || st.HID.MouseOutput != "usb" {
		t.Errorf("unexpected HID state: %+v", st.HID)
	}
	if len(st.HID.MouseOutputs) != 2 {
		t.Errorf("MouseOutputs = %v", st.HID.MouseOutputs)
	}
	if !st.HasResolution || st.Streamer.Resolution.Width != 2560 || st.Streamer.Resolution.Height != 1440 {
		t.Errorf("unexpected streamer state: %+v", st.Streamer)
	}
	if !st.Streamer.JPEGClients || st.Streamer.H264Clients {
		t.Errorf("unexpected client flags: %+v", st.Streamer)
	}
	if res, ok := st.GetResolution(); !ok || res.Width != 2560 || res.Height != 1440 {
		t.Errorf("GetResolution = %+v, %v", res, ok)
	}
	if st.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
	if st.LastPong.IsZero() {
		t.Error("LastPong should be set")
	}
}

func TestStateFoldingEdgeCases(t *testing.T) {
	c := &Client{}

	// loop with a zero version -> empty protocol version (trimVersion 0,0).
	if !c.applyMessage([]byte(`{"event_type":"loop","event":{"version":{"major":0,"minor":0}}}`)) {
		t.Error("loop event should report a change")
	}
	if got := c.ReadState().ProtocolVersion; got != "" {
		t.Errorf("zero-version ProtocolVersion = %q, want empty", got)
	}
	// loop payload that fails to decode.
	if c.applyMessage([]byte(`{"event_type":"loop","event":"nope"}`)) {
		t.Error("bad loop payload should not change state")
	}

	// info without a system block -> no change.
	if c.applyMessage([]byte(`{"event_type":"info","event":{}}`)) {
		t.Error("info without system should not change state")
	}
	// info payload that fails to decode.
	if c.applyMessage([]byte(`{"event_type":"info","event":42}`)) {
		t.Error("bad info payload should not change state")
	}

	// streamer with an explicit null streamer -> no change.
	if c.applyMessage([]byte(`{"event_type":"streamer","event":{"streamer":null}}`)) {
		t.Error("null streamer should not change state")
	}

	// streamer with no resolution: seen, but no resolution yet.
	if !c.applyMessage([]byte(`{"event_type":"streamer","event":{"streamer":{"h264":{"online":false},"source":{"resolution":{"width":0,"height":0}}}}}`)) {
		t.Error("streamer event should report a change")
	}
	st := c.ReadState()
	if !st.StreamerSeen || st.HasResolution {
		t.Errorf("streamer without resolution: seen=%v hasRes=%v", st.StreamerSeen, st.HasResolution)
	}

	// unknown event type and non-JSON garbage are ignored.
	if c.applyMessage([]byte(`{"event_type":"serial","event":{}}`)) {
		t.Error("unknown event should not change state")
	}
	if c.applyMessage([]byte("not json")) {
		t.Error("malformed message should not change state")
	}

	// trimVersion / GetResolution helpers.
	if got := trimVersion(0, 0); got != "" {
		t.Errorf("trimVersion(0,0) = %q, want empty", got)
	}
	if got := trimVersion(4, 82); got != "4.82" {
		t.Errorf("trimVersion(4,82) = %q, want 4.82", got)
	}
	if res, ok := (State{}).GetResolution(); ok || res.Width != 0 {
		t.Errorf("empty GetResolution = %+v, %v, want zero/false", res, ok)
	}
}

// ---------------------------------------------------------------------------
// REST helpers and their error paths
// ---------------------------------------------------------------------------

func TestSetMouseOutputAndPrintDefaults(t *testing.T) {
	f := newCovFake(t)
	c := connectCov(t, f, Options{})

	if err := c.SetMouseOutput("usb_rel"); err != nil {
		t.Fatalf("SetMouseOutput: %v", err)
	}
	// Empty keymap must default to en-us.
	if err := c.PrintWithKeymap("hi", ""); err != nil {
		t.Fatalf("PrintWithKeymap: %v", err)
	}
	if err := c.SetMouseOutput("bogus"); err == nil {
		t.Error("SetMouseOutput(bogus) should error")
	}

	_, queries := f.restCalls()
	if len(queries) < 2 {
		t.Fatalf("expected >=2 REST calls, got %d", len(queries))
	}
	if queries[0]["mouse_output"] != "usb_rel" {
		t.Errorf("set_params query = %v", queries[0])
	}
	if queries[1]["keymap"] != "en-us" {
		t.Errorf("print keymap query = %v", queries[1])
	}
}

func TestRestPostErrorPaths(t *testing.T) {
	t.Run("http status", func(t *testing.T) {
		f := newCovFake(t)
		f.restStatus = http.StatusInternalServerError
		f.restBody = `{"ok":false,"result":{"error":"nope"}}`
		c := connectCov(t, f, Options{})
		if err := c.SetMouseOutput("usb"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Errorf("expected HTTP 500 error, got %v", err)
		}
	})

	t.Run("ok false error", func(t *testing.T) {
		f := newCovFake(t)
		f.restBody = `{"ok":false,"result":{"error":"boom"}}`
		c := connectCov(t, f, Options{})
		if err := c.Print("x"); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("expected boom error, got %v", err)
		}
	})

	t.Run("ok false error_msg", func(t *testing.T) {
		f := newCovFake(t)
		f.restBody = `{"ok":false,"result":{"error_msg":"boom2"}}`
		c := connectCov(t, f, Options{})
		if err := c.Print("x"); err == nil || !strings.Contains(err.Error(), "boom2") {
			t.Errorf("expected boom2 error, got %v", err)
		}
	})

	t.Run("ok false unknown", func(t *testing.T) {
		f := newCovFake(t)
		f.restBody = `{"ok":false,"result":{}}`
		c := connectCov(t, f, Options{})
		if err := c.Print("x"); err == nil || !strings.Contains(err.Error(), "unknown device error") {
			t.Errorf("expected unknown device error, got %v", err)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		c := &Client{baseURL: "http://127.0.0.1:1", token: "t", http: &http.Client{Timeout: time.Second}}
		if _, err := c.restPost("/api/hid/print", nil, nil, ""); err == nil {
			t.Error("restPost with dead endpoint should error")
		}
	})

	t.Run("request build error", func(t *testing.T) {
		c := &Client{baseURL: "http://%zz", token: "t", http: &http.Client{Timeout: time.Second}}
		if _, err := c.restPost("/api/hid/print", nil, nil, ""); err == nil {
			t.Error("restPost with malformed URL should error")
		}
	})
}

func TestTypeTextPaths(t *testing.T) {
	// No newline: delegated straight to Print.
	f := newCovFake(t)
	c := connectCov(t, f, Options{})
	if err := c.TypeText("abc"); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	bodies, _ := f.restCalls()
	if len(bodies) != 1 || bodies[0] != "abc" {
		t.Errorf("TypeText bodies = %v, want [abc]", bodies)
	}

	// Leading/empty line must be skipped but still emit an Enter tap.
	f2 := newCovFake(t)
	c2 := connectCov(t, f2, Options{})
	if err := c2.TypeText("\n"); err != nil {
		t.Fatalf("TypeText newline: %v", err)
	}
	f2.waitEvents(1)
	if len(f2.eventsOf("key")) == 0 {
		t.Error("TypeText(\"\\n\") should emit an Enter tap")
	}

	// Print failure aborts before the Enter tap.
	f3 := newCovFake(t)
	f3.restStatus = http.StatusInternalServerError
	c3 := connectCov(t, f3, Options{})
	if err := c3.TypeText("a\nb"); err == nil {
		t.Error("TypeText should fail when Print fails")
	}

	// Tap failure: REST stays up, but the socket is closed.
	f4 := newCovFake(t)
	c4 := connectCov(t, f4, Options{})
	if _, err := c4.WaitFor(func(s State) bool { return s.HIDSeen }, 3*time.Second); err != nil {
		t.Fatalf("wait hid: %v", err)
	}
	_ = c4.Close()
	if err := c4.TypeText("a\nb"); err == nil {
		t.Error("TypeText should fail when the Enter tap fails")
	}
}

// Ensure math is used (keeps the import meaningful if helpers change) by
// verifying Infinity fails to marshal where the client expects.
func TestNonJSONPayloadRejected(t *testing.T) {
	c := &Client{done: make(chan struct{})}
	if err := c.writeJSON("x", math.Inf(1)); err == nil {
		t.Error("writeJSON with +Inf should error")
	}
}
