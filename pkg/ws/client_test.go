package ws

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestResolveKey(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"KeyA", "KeyA", false},
		{"a", "KeyA", false},
		{"A", "KeyA", false},
		{"1", "Digit1", false},
		{"Digit0", "Digit0", false},
		{"ctrl", "ControlLeft", false},
		{"CTRL", "ControlLeft", false},
		{"rctrl", "ControlRight", false},
		{"alt", "AltLeft", false},
		{"del", "Delete", false},
		{"Delete", "Delete", false},
		{"enter", "Enter", false},
		{"Return", "Enter", false},
		{"esc", "Escape", false},
		{"pgup", "PageUp", false},
		{"down", "ArrowDown", false},
		{"caps", "CapsLock", false},
		{"f5", "F5", false},
		{"F24", "F24", false},
		{"cmd", "MetaLeft", false},
		{"super", "MetaLeft", false},
		{"space", "Space", false},
		{"", "", true},
		{"NotAKey", "", true},
		{"f25", "", true},
		{"ctrl+alt", "", true},
	}
	for _, tc := range tests {
		got, err := ResolveKey(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ResolveKey(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveKey(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveCombo(t *testing.T) {
	got, err := ResolveCombo([]string{"ctrl", "alt", "del"})
	if err != nil {
		t.Fatalf("ResolveCombo error: %v", err)
	}
	want := []string{"ControlLeft", "AltLeft", "Delete"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if _, err := ResolveCombo(nil); err == nil {
		t.Error("ResolveCombo(nil) should error")
	}
	if _, err := ResolveCombo([]string{"ctrl", "bogus"}); err == nil {
		t.Error("ResolveCombo with bogus key should error")
	}
}

func TestPixelToAbsAndPercent(t *testing.T) {
	// Center of a 1920x1080 frame maps to (0,0).
	x, y := PixelToAbs(960, 540, 1920, 1080)
	if x != 0 || y != 0 {
		t.Errorf("center = (%d,%d), want (0,0)", x, y)
	}
	// Top-left maps to -32768.
	x, y = PixelToAbs(0, 0, 1920, 1080)
	if x != -32768 || y != -32768 {
		t.Errorf("top-left = (%d,%d), want (-32768,-32768)", x, y)
	}
	// Bottom-right (px == dim) maps to 32767 (clamped).
	x, y = PixelToAbs(1920, 1080, 1920, 1080)
	if x != 32767 || y != 32767 {
		t.Errorf("bottom-right = (%d,%d), want (32767,32767)", x, y)
	}
	// Unknown resolution returns 0.
	if x, _ := PixelToAbs(100, 100, 0, 0); x != 0 {
		t.Errorf("zero dim = %d, want 0", x)
	}
	if x := scalePercent(50); x != 0 {
		t.Errorf("scalePercent(50) = %d, want 0", x)
	}
	if x := scalePercent(0); x != -32768 {
		t.Errorf("scalePercent(0) = %d, want -32768", x)
	}
	if x := scalePercent(100); x != 32767 {
		t.Errorf("scalePercent(100) = %d, want 32767", x)
	}
	if x := scalePercent(200); x != 32767 {
		t.Errorf("scalePercent(200) = %d, want 32767 (clamped)", x)
	}
}

func TestBuildWSURL(t *testing.T) {
	got, err := buildWSURL("https://glkvm.example.com", "tok123", true)
	if err != nil {
		t.Fatalf("buildWSURL error: %v", err)
	}
	want := "wss://glkvm.example.com/api/ws?auth_token=tok123&stream=1"
	if got != want {
		t.Errorf("buildWSURL = %q, want %q", got, want)
	}
	// http -> ws
	got, _ = buildWSURL("http://host/", "t", false)
	if got != "ws://host/api/ws?auth_token=t" {
		t.Errorf("buildWSURL http = %q", got)
	}
	// bare host defaults to https/wss
	got, _ = buildWSURL("host", "t", true)
	if got != "wss://host/api/ws?auth_token=t&stream=1" {
		t.Errorf("buildWSURL bare = %q", got)
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("wss://h/api/ws?auth_token=secret&stream=1")
	if got == "" || got == "wss://h/api/ws?auth_token=secret&stream=1" {
		t.Errorf("redactURL did not redact: %q", got)
	}
}

func TestApplyMessageParsing(t *testing.T) {
	c := &Client{}
	hidMsg := `{"event_type":"hid","event":{"enabled":true,"online":true,"keyboard":{"online":true,"leds":{"caps":true,"num":false,"scroll":false}},"mouse":{"online":true,"absolute":true,"outputs":{"active":"usb","available":["usb","usb_rel"]}}}}`
	if !c.applyMessage([]byte(hidMsg)) {
		t.Fatal("applyMessage returned false for hid event")
	}
	st := c.ReadState()
	if !st.HIDSeen || !st.HID.CapsLock || !st.HID.Absolute || st.HID.MouseOutput != "usb" {
		t.Fatalf("unexpected hid state: %+v", st.HID)
	}
	if len(st.HID.MouseOutputs) != 2 {
		t.Fatalf("mouse outputs = %v", st.HID.MouseOutputs)
	}

	strMsg := `{"event_type":"streamer","event":{"streamer":{"h264":{"online":true},"sinks":{"jpeg":{"has_clients":true},"h264":{"has_clients":true}},"source":{"resolution":{"width":2560,"height":1440}}}}}`
	if !c.applyMessage([]byte(strMsg)) {
		t.Fatal("applyMessage returned false for streamer event")
	}
	st = c.ReadState()
	if !st.HasResolution || st.Streamer.Resolution.Width != 2560 || st.Streamer.Resolution.Height != 1440 {
		t.Fatalf("unexpected streamer state: %+v", st.Streamer)
	}
	if !st.Streamer.JPEGClients {
		t.Error("expected jpeg clients true")
	}

	loopMsg := `{"event_type":"loop","event":{"version":{"major":4,"minor":82}}}`
	c.applyMessage([]byte(loopMsg))
	if st := c.ReadState(); st.ProtocolVersion != "4.82" {
		t.Errorf("protocol version = %q", st.ProtocolVersion)
	}

	// Malformed and unknown messages must not panic or change hid state.
	if c.applyMessage([]byte("not json")) {
		t.Error("malformed message reported change")
	}
	if c.applyMessage([]byte(`{"event_type":"serial","event":{"exist":false}}`)) {
		t.Error("unknown event reported change")
	}
}

func TestInvalidMouseInputs(t *testing.T) {
	c := &Client{}
	if err := c.MouseButton("bogus", true); err == nil {
		t.Error("MouseButton(bogus) should error")
	}
	if err := c.SetMouseOutput("bogus"); err == nil {
		t.Error("SetMouseOutput(bogus) should error")
	}
}

func TestValidKeysNonEmpty(t *testing.T) {
	keys := ValidKeys()
	if len(keys) < 100 {
		t.Fatalf("ValidKeys returned %d keys, expected the full DOM set", len(keys))
	}
	if !IsValidKey("KeyA") || IsValidKey("Nope") {
		t.Error("IsValidKey misbehaved")
	}
}

// TestLiveWSRoundTrip exercises the real device when KVM_URL/KVM_USERNAME/
// KVM_PASSWORD are set (integration-style). It is skipped otherwise so the
// standard unit suite never touches the network.
func TestLiveWSRoundTrip(t *testing.T) {
	url := os.Getenv("KVM_URL")
	user := os.Getenv("KVM_USERNAME")
	pass := os.Getenv("KVM_PASSWORD")
	if url == "" || user == "" || pass == "" {
		t.Skip("KVM_URL/KVM_USERNAME/KVM_PASSWORD not set; skipping live WS test")
	}

	tok, err := loginForTest(url, user, pass)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	c, err := Connect(url, tok, Options{Insecure: true, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.WaitFor(func(s State) bool { return s.HIDSeen }, 5*time.Second); err != nil {
		t.Fatalf("wait hid: %v", err)
	}
	if _, err := c.Resolution(5 * time.Second); err != nil {
		t.Fatalf("resolution: %v", err)
	}

	before := c.ReadState().HID.CapsLock
	if err := c.TapKey("CapsLock"); err != nil {
		t.Fatalf("tap caps: %v", err)
	}
	// Read the authoritative state over REST; the WS hid event follows.
	time.Sleep(700 * time.Millisecond)
	after := restCaps(t, url, tok)
	if after == before {
		t.Fatalf("caps did not toggle (before=%v after=%v)", before, after)
	}
	// Toggle back.
	if err := c.TapKey("CapsLock"); err != nil {
		t.Fatalf("tap caps back: %v", err)
	}
	time.Sleep(700 * time.Millisecond)
	if got := restCaps(t, url, tok); got != before {
		t.Fatalf("caps not restored: got %v want %v", got, before)
	}

	if err := c.Combo([]string{"ctrl", "alt", "del"}); err != nil {
		t.Fatalf("combo: %v", err)
	}
	if err := c.MouseMoveAbs(0, 0); err != nil {
		t.Fatalf("mouse move: %v", err)
	}
	if err := c.Print(" "); err != nil {
		t.Fatalf("print: %v", err)
	}
}

func loginForTest(baseURL, user, pass string) (string, error) {
	return doLogin(baseURL, user, pass)
}

// insecureHTTPClient returns an HTTP client that skips TLS verification.
func insecureHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec
	}
}

func doLogin(baseURL, user, pass string) (string, error) {
	form := url.Values{}
	form.Set("user", user)
	form.Set("passwd", pass)
	resp, err := insecureHTTPClient().PostForm(normalizeBaseURL(baseURL)+"/api/auth/login", form)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var env struct {
		Result struct {
			Token string `json:"token"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return "", err
	}
	if env.Result.Token == "" {
		return "", fmt.Errorf("login returned no token: %s", string(data))
	}
	return env.Result.Token, nil
}

func getJSON(baseURL, token, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, normalizeBaseURL(baseURL)+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("token", token)
	resp, err := insecureHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}

// helper indirection kept small; implementation lives in live_test_impl.go style
// inline below.
func restCaps(t *testing.T, baseURL, token string) bool {
	t.Helper()
	body, err := getJSON(baseURL, token, "/api/hid")
	if err != nil {
		t.Fatalf("get hid: %v", err)
	}
	var env struct {
		Result struct {
			Keyboard struct {
				Leds struct {
					Caps bool `json:"caps"`
				} `json:"leds"`
			} `json:"keyboard"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode hid: %v", err)
	}
	return env.Result.Keyboard.Leds.Caps
}
