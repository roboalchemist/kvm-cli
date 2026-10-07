// Package ws implements a small WebSocket client for the GL.iNet Comet (PiKVM)
// /api/ws HID control channel.
//
// The device accepts HID events either as JSON text frames
// ({"event_type":"key","event":{...}}) or as a compact binary frame format. This
// client uses the JSON text form, which is the easiest to reason about and is
// accepted for every event type.
//
// On connect the server sends a "loop" event followed by per-subsystem state
// events (notably "hid" and "streamer"). A background reader folds those events
// into a State snapshot; callers can retrieve it with ReadState or subscribe
// with SetOnState. A background keepalive sends {"event_type":"ping"} every two
// seconds.
//
// The client is safe for concurrent use: writes are serialised and state is
// guarded by a mutex.
package ws

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

const (
	// defaultTimeout bounds the WebSocket handshake and REST helper requests.
	defaultTimeout = 30 * time.Second
	// defaultPingInterval is the keepalive cadence.
	defaultPingInterval = 2 * time.Second
	// defaultWaitResolution is how long MouseMovePixels waits for the streamer
	// to report a resolution before giving up.
	defaultWaitResolution = 3 * time.Second
)

// Options configures a Client.
type Options struct {
	// Insecure disables TLS certificate verification. The Comet commonly
	// presents a self-signed certificate, so callers usually enable this.
	Insecure bool
	// Timeout bounds the WebSocket handshake and REST helper requests. A
	// non-positive value uses 30s.
	Timeout time.Duration
	// Debug enables verbose logging to stderr.
	Debug bool
}

// Client is a WebSocket HID control channel to a KVM device.
type Client struct {
	baseURL string
	token   string
	opts    Options

	conn      *websocket.Conn
	http      *http.Client
	writeMu   sync.Mutex
	closeOnce sync.Once

	mu      sync.Mutex
	state   State
	onState func(State)
	err     error

	done chan struct{}
	wg   sync.WaitGroup

	pingInterval time.Duration
}

// Connect dials the /api/ws channel for baseURL using token and starts the
// background reader and keepalive goroutines. The connection includes
// stream=1 by default so that the JPEG snapshot sink is kept warm.
func Connect(baseURL, token string, opts Options) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("websocket connect: empty base URL")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("websocket connect: empty auth token")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	wsURL, err := buildWSURL(baseURL, token, true)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: opts.Insecure} //nolint:gosec // self-signed device cert
	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
		TLSClientConfig:  tlsConfig,
	}

	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("websocket dial %s: HTTP %d: %w", redactURL(wsURL), resp.StatusCode, err)
		}
		return nil, fmt.Errorf("websocket dial %s: %w", redactURL(wsURL), err)
	}

	c := &Client{
		baseURL: normalizeBaseURL(baseURL),
		token:   token,
		opts:    opts,
		conn:    conn,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
		done:         make(chan struct{}),
		pingInterval: defaultPingInterval,
	}

	c.wg.Add(2)
	go c.readLoop()
	go c.keepalive()
	c.debugf("ws: connected to %s", redactURL(wsURL))
	return c, nil
}

// buildWSURL converts an HTTP(S) base URL into a ws(s) /api/ws URL carrying the
// auth token and, optionally, stream=1.
func buildWSURL(baseURL, token string, stream bool) (string, error) {
	u, err := url.Parse(normalizeBaseURL(baseURL))
	if err != nil {
		// url.Parse embeds the raw URL in its error and baseURL may carry
		// userinfo, so redact the whole message (not just the %q argument).
		return "", redact.Error(fmt.Errorf("invalid base URL %q: %w", baseURL, err))
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		u.Scheme = "wss"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/ws"
	q := u.Query()
	q.Set("auth_token", token)
	if stream {
		q.Set("stream", "1")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// normalizeBaseURL trims whitespace and a trailing slash and defaults the scheme
// to https when a bare host is supplied.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	return strings.TrimRight(raw, "/")
}

// redactURL removes the auth_token query value from a URL for safe logging,
// and masks any URL userinfo (scheme://user:password@host) it may carry.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		// An unparseable URL can still embed userinfo; mask best-effort.
		return redact.URL(raw)
	}
	q := u.Query()
	if q.Has("auth_token") {
		q.Set("auth_token", "***")
	}
	u.RawQuery = q.Encode()
	return redact.URL(u.String())
}

// BaseURL returns the normalised base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// Done returns a channel closed when the client is closed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns the terminal connection error, if any.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// ReadState returns a snapshot of the latest server state.
func (c *Client) ReadState() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// SetOnState registers a callback invoked (outside the state lock) whenever the
// observed state changes. Passing nil clears the callback.
func (c *Client) SetOnState(fn func(State)) {
	c.mu.Lock()
	c.onState = fn
	c.mu.Unlock()
}

// WaitFor polls the state until pred returns true or the timeout elapses. It
// returns the last observed state regardless.
func (c *Client) WaitFor(pred func(State) bool, timeout time.Duration) (State, error) {
	deadline := time.Now().Add(timeout)
	for {
		st := c.ReadState()
		if pred(st) {
			return st, nil
		}
		if time.Now().After(deadline) {
			return st, fmt.Errorf("timed out after %s waiting for device state", timeout)
		}
		select {
		case <-c.done:
			return c.ReadState(), fmt.Errorf("websocket closed while waiting for device state: %w", c.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Resolution waits up to timeout for the streamer to report a resolution.
func (c *Client) Resolution(timeout time.Duration) (Resolution, error) {
	if timeout <= 0 {
		timeout = defaultWaitResolution
	}
	st, err := c.WaitFor(func(s State) bool { return s.HasResolution }, timeout)
	if err != nil {
		return Resolution{}, err
	}
	return st.Streamer.Resolution, nil
}

func (c *Client) setErr(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

func (c *Client) debugf(format string, args ...any) {
	if c.opts.Debug {
		fmt.Fprintf(os.Stderr, "[debug] ws: "+format+"\n", args...)
	}
}

// readLoop consumes server messages and folds them into the state until the
// connection closes.
func (c *Client) readLoop() {
	defer c.wg.Done()
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			select {
			case <-c.done:
				// Normal close.
			default:
				c.setErr(fmt.Errorf("websocket read: %w", err))
				c.debugf("read loop ended: %v", err)
			}
			return
		}
		c.applyMessage(data)
	}
}

// keepalive periodically pings the server so the socket stays open.
func (c *Client) keepalive() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := c.sendPing(); err != nil {
				c.debugf("keepalive ping failed: %v", err)
				return
			}
		}
	}
}

// Close terminates the connection and waits for the background goroutines. It is
// safe to call multiple times.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
	c.wg.Wait()
	return nil
}

// ---- low-level send helpers ------------------------------------------------

func (c *Client) writeJSON(eventType string, event any) error {
	payload, err := json.Marshal(outboundMessage{EventType: eventType, Event: event})
	if err != nil {
		return fmt.Errorf("encode %s event: %w", eventType, err)
	}
	return c.writeFrame(websocket.TextMessage, payload)
}

func (c *Client) writeFrame(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return fmt.Errorf("websocket is closed")
	default:
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout())); err != nil {
		return err
	}
	if err := c.conn.WriteMessage(messageType, data); err != nil {
		return fmt.Errorf("websocket write: %w", err)
	}
	return nil
}

func (c *Client) writeTimeout() time.Duration {
	if c.opts.Timeout > 0 {
		return c.opts.Timeout
	}
	return defaultTimeout
}

// outboundMessage is the JSON envelope for every client -> server event.
type outboundMessage struct {
	EventType string `json:"event_type"`
	Event     any    `json:"event"`
}

// ---- HID events ------------------------------------------------------------

type keyEvent struct {
	Key    string `json:"key"`
	State  bool   `json:"state"`
	Finish bool   `json:"finish"`
}

type point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type buttonEvent struct {
	Button string `json:"button"`
	State  bool   `json:"state"`
}

type absMoveEvent struct {
	To point `json:"to"`
}

type deltaEvent struct {
	Delta  point `json:"delta"`
	Squash bool  `json:"squash,omitempty"`
}

// sendPing writes a ping event.
func (c *Client) sendPing() error {
	return c.writeJSON("ping", struct{}{})
}

// PressKey holds a key down (no finish bit).
func (c *Client) PressKey(name string) error { return c.sendKey(name, true, false) }

// ReleaseKey releases a key.
func (c *Client) ReleaseKey(name string) error { return c.sendKey(name, false, false) }

// TapKey presses and releases a key in one frame using the finish bit, which
// auto-releases non-modifier keys on a press. This is the one-shot "type a key"
// primitive.
func (c *Client) TapKey(name string) error { return c.sendKey(name, true, true) }

func (c *Client) sendKey(name string, state, finish bool) error {
	code, err := ResolveKey(name)
	if err != nil {
		return err
	}
	c.debugf("key %s state=%v finish=%v", code, state, finish)
	return c.writeJSON("key", keyEvent{Key: code, State: state, Finish: finish})
}

// Combo sends an ordered key combination: every key is pressed in order, then
// released in reverse order. For example ["ctrl","alt","del"] becomes
// ControlLeft↓ AltLeft↓ Delete↓ Delete↑ AltLeft↑ ControlLeft↑.
func (c *Client) Combo(keys []string) error {
	codes, err := ResolveCombo(keys)
	if err != nil {
		return err
	}
	for _, code := range codes {
		if err := c.sendKey(code, true, false); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i := len(codes) - 1; i >= 0; i-- {
		if err := c.sendKey(codes[i], false, false); err != nil {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// validMouseButtons are the button names accepted by the device.
var validMouseButtons = map[string]struct{}{
	"left": {}, "middle": {}, "right": {}, "up": {}, "down": {},
}

// MouseButton presses or releases a mouse button.
func (c *Client) MouseButton(button string, down bool) error {
	b := strings.ToLower(strings.TrimSpace(button))
	if _, ok := validMouseButtons[b]; !ok {
		return fmt.Errorf("unknown mouse button %q (expected left|middle|right|up|down)", button)
	}
	return c.writeJSON("mouse_button", buttonEvent{Button: b, State: down})
}

// MouseClick presses then releases a mouse button.
func (c *Client) MouseClick(button string) error {
	if err := c.MouseButton(button, true); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	return c.MouseButton(button, false)
}

// MouseMoveAbs moves the pointer to an absolute position in device coordinates
// (signed int16, -32768..32767). Use MouseMovePixels or MouseMovePct to work in
// pixel or percentage space.
func (c *Client) MouseMoveAbs(x, y int16) error {
	c.debugf("mouse_move abs x=%d y=%d", x, y)
	return c.writeJSON("mouse_move", absMoveEvent{To: point{X: int(x), Y: int(y)}})
}

// MouseMovePct moves the pointer to a percentage of the frame (0..100 on each
// axis). It does not require the frame resolution.
func (c *Client) MouseMovePct(xPct, yPct float64) error {
	x := scalePercent(xPct)
	y := scalePercent(yPct)
	return c.MouseMoveAbs(x, y)
}

// MouseMovePixels moves the pointer to an absolute pixel position, converting
// using the frame resolution reported by the streamer. It waits up to
// defaultWaitResolution for a resolution to become known.
func (c *Client) MouseMovePixels(xPx, yPx int) error {
	res, err := c.Resolution(defaultWaitResolution)
	if err != nil {
		return fmt.Errorf("mouse move: %w", err)
	}
	x, y := PixelToAbs(xPx, yPx, res.Width, res.Height)
	return c.MouseMoveAbs(x, y)
}

// MouseRelative moves the pointer by a relative delta. Values are clamped to the
// signed 8-bit range the device expects for relative motion.
func (c *Client) MouseRelative(dx, dy int) error {
	return c.writeJSON("mouse_relative", deltaEvent{
		Delta: point{X: clampInt8(dx), Y: clampInt8(dy)},
	})
}

// MouseWheel scrolls by a relative delta. Values are clamped to the signed
// 8-bit range the device expects.
func (c *Client) MouseWheel(dx, dy int) error {
	return c.writeJSON("mouse_wheel", deltaEvent{
		Delta: point{X: clampInt8(dx), Y: clampInt8(dy)},
	})
}

// PixelToAbs converts a pixel coordinate on a frame of the given dimensions to
// the device's signed int16 absolute coordinate space:
//
//	abs = clamp(round((px / dim) * 65535) - 32768, -32768, 32767)
func PixelToAbs(px, py, width, height int) (int16, int16) {
	return scaleFraction(px, width), scaleFraction(py, height)
}

func scalePercent(pct float64) int16 {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return clampInt16(int(round((pct/100.0)*65535.0) - 32768.0))
}

func scaleFraction(px, dim int) int16 {
	if dim <= 0 {
		return 0
	}
	return clampInt16(int(round((float64(px)/float64(dim))*65535.0) - 32768.0))
}

func round(f float64) float64 {
	if f < 0 {
		return float64(int(f - 0.5))
	}
	return float64(int(f + 0.5))
}

func clampInt16(v int) int16 {
	if v < -32768 {
		return -32768
	}
	if v > 32767 {
		return 32767
	}
	return int16(v)
}

func clampInt8(v int) int {
	if v < -128 {
		return -128
	}
	if v > 127 {
		return 127
	}
	return v
}

// ---- REST helpers ----------------------------------------------------------

// validMouseOutputs are the mouse output modes accepted by /api/hid/set_params.
var validMouseOutputs = map[string]struct{}{
	"usb": {}, "usb_rel": {}, "usb_hybrid": {}, "usb_touch": {},
}

// SetMouseOutput switches the mouse between absolute ("usb") and relative
// ("usb_rel") modes (also usb_hybrid, usb_touch) via
// POST /api/hid/set_params?mouse_output=<mode>.
func (c *Client) SetMouseOutput(mode string) error {
	m := strings.TrimSpace(mode)
	if _, ok := validMouseOutputs[m]; !ok {
		return fmt.Errorf("invalid mouse output %q (expected usb|usb_rel|usb_hybrid|usb_touch)", mode)
	}
	q := url.Values{}
	q.Set("mouse_output", m)
	_, err := c.restPost("/api/hid/set_params", q, nil, "")
	return err
}

// Print types text on the target using the device's layout-aware
// POST /api/hid/print endpoint with the default "en-us" keymap.
func (c *Client) Print(text string) error {
	return c.PrintWithKeymap(text, "en-us")
}

// PrintWithKeymap is Print with an explicit keymap.
func (c *Client) PrintWithKeymap(text, keymap string) error {
	if keymap == "" {
		keymap = "en-us"
	}
	q := url.Values{}
	q.Set("limit", "0")
	q.Set("keymap", keymap)
	_, err := c.restPost("/api/hid/print", q, []byte(text), "text/plain; charset=utf-8")
	return err
}

// TypeText types a string on the target. Newlines are mapped to Enter taps so
// multi-line text is typed correctly; the rest is delegated to the layout-aware
// print endpoint.
func (c *Client) TypeText(s string) error {
	if !strings.Contains(s, "\n") {
		return c.Print(s)
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			if err := c.Print(line); err != nil {
				return err
			}
		}
		if i < len(lines)-1 {
			if err := c.TapKey("Enter"); err != nil {
				return err
			}
		}
	}
	return nil
}

// restPost issues an authenticated POST and validates the {"ok","result"}
// envelope.
func (c *Client) restPost(path string, query url.Values, body []byte, contentType string) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, u, reader)
	if err != nil {
		// The composed URL may carry userinfo; url.Error embeds the raw URL.
		return nil, redact.Error(fmt.Errorf("create request: %w", err))
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("token", c.token)

	c.debugf("POST %s (%d bytes)", redactURL(u), len(body))
	resp, err := c.http.Do(req)
	if err != nil {
		// http.Client.Do returns a *url.Error embedding the full URL, which may
		// carry userinfo; redact before it escapes.
		return nil, redact.Error(fmt.Errorf("POST %s: %w", path, err))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Error    string `json:"error"`
			ErrorMsg string `json:"error_msg"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &env); err == nil && !env.OK {
		msg := env.Result.ErrorMsg
		if msg == "" {
			msg = env.Result.Error
		}
		if msg == "" {
			msg = "unknown device error"
		}
		return nil, fmt.Errorf("POST %s: %s", path, msg)
	}
	return data, nil
}
