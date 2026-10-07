package ws

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeKVM is an in-process WebSocket + REST server that emulates the parts of
// the device the ws.Client talks to.
type fakeKVM struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	conn       *websocket.Conn
	events     []map[string]any
	restBodies []string
	restQuery  []map[string]string
}

func newFakeKVM(t *testing.T) *fakeKVM {
	t.Helper()
	f := &fakeKVM{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", f.handleWS)
	mux.HandleFunc("/api/hid/set_params", f.handleREST)
	mux.HandleFunc("/api/hid/print", f.handleREST)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKVM) URL() string { return f.srv.URL }

func (f *fakeKVM) handleWS(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		f.t.Logf("upgrade: %v", err)
		return
	}
	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
	defer func() { _ = c.Close() }()

	if auth := r.URL.Query().Get("auth_token"); auth == "" {
		f.t.Error("missing auth_token query parameter")
	}

	handshake := []string{
		`{"event_type":"loop","event":{"version":{"major":4,"minor":82}}}`,
		`{"event_type":"hid","event":{"enabled":true,"online":true,"connected":true,"keyboard":{"online":true,"leds":{"caps":false,"num":false,"scroll":false},"outputs":{"active":"","available":[]}},"mouse":{"online":true,"absolute":true,"outputs":{"active":"usb","available":["usb","usb_rel","usb_hybrid","usb_touch"]}}}}`,
		`{"event_type":"streamer","event":{"streamer":{"h264":{"online":true,"fps":60},"sinks":{"jpeg":{"has_clients":true},"h264":{"has_clients":true}},"source":{"resolution":{"width":2560,"height":1440}}}}}`,
	}
	for _, h := range handshake {
		if err := c.WriteMessage(websocket.TextMessage, []byte(h)); err != nil {
			return
		}
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

		if ev["event_type"] == "ping" {
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"event_type":"pong","event":{}}`))
		}
	}
}

func (f *fakeKVM) handleREST(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	f.mu.Lock()
	f.restBodies = append(f.restBodies, body)
	q := map[string]string{}
	for k := range r.URL.Query() {
		q[k] = r.URL.Query().Get(k)
	}
	f.restQuery = append(f.restQuery, q)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
}

// waitEvents waits until at least n events have been recorded.
func (f *fakeKVM) waitEvents(n int) []map[string]any {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.events)
		f.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.events))
	copy(out, f.events)
	return out
}

func (f *fakeKVM) eventsByType(t string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, e := range f.events {
		if e["event_type"] == t {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeKVM) restCalls() ([]string, []map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bodies := append([]string(nil), f.restBodies...)
	queries := append([]map[string]string(nil), f.restQuery...)
	return bodies, queries
}

func connectFake(t *testing.T, f *fakeKVM) *Client {
	t.Helper()
	c, err := Connect(f.URL(), "testtoken", Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.WaitFor(func(s State) bool { return s.HIDSeen }, 3*time.Second); err != nil {
		t.Fatalf("wait hid: %v", err)
	}
	if _, err := c.Resolution(3 * time.Second); err != nil {
		t.Fatalf("resolution: %v", err)
	}
	return c
}

func TestFakeKeyEvents(t *testing.T) {
	f := newFakeKVM(t)
	c := connectFake(t, f)

	if err := c.PressKey("KeyA"); err != nil {
		t.Fatalf("press: %v", err)
	}
	if err := c.ReleaseKey("KeyA"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := c.TapKey("ctrl"); err != nil {
		t.Fatalf("tap: %v", err)
	}
	if err := c.Combo([]string{"ctrl", "alt", "del"}); err != nil {
		t.Fatalf("combo: %v", err)
	}

	evs := f.waitEvents(7) // press, release, tap, 3 combo down + 3 up = 8; allow >=7
	if len(evs) < 7 {
		t.Fatalf("expected >=7 events, got %d", len(evs))
	}
	keys := f.eventsByType("key")
	if len(keys) < 6 {
		t.Fatalf("expected >=6 key events, got %d", len(keys))
	}
	// TapKey("ctrl") must use the finish bit.
	foundTap := false
	for _, k := range keys {
		ev := k["event"].(map[string]any)
		if ev["key"] == "ControlLeft" && ev["state"] == true && ev["finish"] == true {
			foundTap = true
		}
	}
	if !foundTap {
		t.Error("expected a ControlLeft tap with finish=true")
	}
}

func TestFakeMouseEvents(t *testing.T) {
	f := newFakeKVM(t)
	c := connectFake(t, f)

	if err := c.MouseMoveAbs(1000, -500); err != nil {
		t.Fatalf("move abs: %v", err)
	}
	if err := c.MouseMovePct(50, 50); err != nil {
		t.Fatalf("move pct: %v", err)
	}
	if err := c.MouseMovePixels(1280, 720); err != nil {
		t.Fatalf("move px: %v", err)
	}
	if err := c.MouseClick("left"); err != nil {
		t.Fatalf("click: %v", err)
	}
	if err := c.MouseRelative(5, -5); err != nil {
		t.Fatalf("rel: %v", err)
	}
	if err := c.MouseWheel(0, -3); err != nil {
		t.Fatalf("wheel: %v", err)
	}

	f.waitEvents(7)
	moves := f.eventsByType("mouse_move")
	if len(moves) != 3 {
		t.Fatalf("expected 3 mouse_move events, got %d", len(moves))
	}
	first := moves[0]["event"].(map[string]any)["to"].(map[string]any)
	if first["x"].(float64) != 1000 || first["y"].(float64) != -500 {
		t.Errorf("abs move to %v", first)
	}
	// Pct(50,50) -> center -> 0,0.
	second := moves[1]["event"].(map[string]any)["to"].(map[string]any)
	if second["x"].(float64) != 0 || second["y"].(float64) != 0 {
		t.Errorf("pct move to %v", second)
	}
	// Pixels(1280,720) on 2560x1440 -> center -> 0,0.
	third := moves[2]["event"].(map[string]any)["to"].(map[string]any)
	if third["x"].(float64) != 0 || third["y"].(float64) != 0 {
		t.Errorf("px move to %v", third)
	}

	buttons := f.eventsByType("mouse_button")
	if len(buttons) != 2 {
		t.Fatalf("expected 2 mouse_button events, got %d", len(buttons))
	}
}

func TestFakeRESTHelpers(t *testing.T) {
	f := newFakeKVM(t)
	c := connectFake(t, f)

	if err := c.SetMouseOutput("usb_rel"); err != nil {
		t.Fatalf("set output: %v", err)
	}
	if err := c.Print("hello"); err != nil {
		t.Fatalf("print: %v", err)
	}
	if err := c.PrintWithKeymap("guten tag", "de"); err != nil {
		t.Fatalf("print keymap: %v", err)
	}
	if err := c.TypeText("line1\nline2"); err != nil {
		t.Fatalf("type: %v", err)
	}

	bodies, queries := f.restCalls()
	if len(bodies) != 5 {
		t.Fatalf("expected 5 REST calls, got %d (bodies=%v)", len(bodies), bodies)
	}
	if queries[0]["mouse_output"] != "usb_rel" {
		t.Errorf("set_params query = %v", queries[0])
	}
	if bodies[1] != "hello" {
		t.Errorf("print body = %q", bodies[1])
	}
	if queries[2]["keymap"] != "de" {
		t.Errorf("keymap query = %v", queries[2])
	}
	if bodies[3] != "line1" || bodies[4] != "line2" {
		t.Errorf("TypeText bodies = %q, %q", bodies[3], bodies[4])
	}
	// TypeText with a newline: print line1, Enter tap, print line2 and the
	// Enter tap must be a key event.
	if len(f.eventsByType("key")) == 0 {
		t.Error("expected an Enter key event from TypeText newline handling")
	}
}

func TestFakeStateCallback(t *testing.T) {
	f := newFakeKVM(t)
	got := make(chan State, 8)
	c, err := Connect(f.URL(), "tok", Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()
	c.SetOnState(func(s State) {
		select {
		case got <- s:
		default:
		}
	})
	select {
	case s := <-got:
		_ = s // first callback may be the "loop" event
	case <-time.After(3 * time.Second):
		t.Fatal("state callback never fired")
	}
	// Later callbacks carry the hid/streamer state.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-got:
			if s.HIDSeen {
				return
			}
		case <-deadline:
			t.Fatal("HID state callback never fired")
		}
	}
}

func TestFakeKeepalive(t *testing.T) {
	f := newFakeKVM(t)
	c, err := Connect(f.URL(), "tok", Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	// The default keepalive cadence is 2s; allow a comfortable margin.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.eventsByType("ping")) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(f.eventsByType("ping")) == 0 {
		t.Fatal("keepalive never sent a ping")
	}
	// The pong should update LastPong.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !c.ReadState().LastPong.IsZero() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c.ReadState().LastPong.IsZero() {
		t.Error("LastPong never updated after ping/pong")
	}
}
