package cmd

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/crop"
	"github.com/roboalchemist/kvm-cli/pkg/elements"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/roboalchemist/kvm-cli/pkg/scratch"
	"github.com/roboalchemist/kvm-cli/pkg/ws"
	"github.com/spf13/cobra"
)

// shot flags.
var (
	flagShotOutput         string
	flagShotFrames         int
	flagShotKeepAlive      bool
	flagShotRegion         string
	flagShotScale          float64
	flagShotUntilStable    bool
	flagShotStableInterval time.Duration
	flagShotStableTimeout  time.Duration
)

// shotOptions carries the capture parameters shared by 'screenshot' and
// 'streamer snapshot' (which supplies the output/frames/keep-alive subset).
type shotOptions struct {
	Output         string
	Frames         int
	KeepAlive      bool
	Region         *elements.Region
	Scale          float64
	UntilStable    bool
	StableInterval time.Duration
	StableTimeout  time.Duration
}

var screenshotCmd = &cobra.Command{
	Use:   "screenshot",
	Short: "Capture a JPEG screenshot of the target",
	Long: `Capture a JPEG frame from the KVM's video stream.

The device only produces snapshots while a streaming client is connected
(GET /api/streamer/snapshot returns HTTP 503 otherwise), so this command opens
a short-lived stream WebSocket, waits for the streamer to come up, fetches the
snapshot, and closes the socket.

By default the JPEG is written to a unique, timestamped file under the scratch
directory (the OS temp directory unless --scratch-dir, $KVM_SCRATCH_DIR or the
config 'scratch_dir' key says otherwise) — screenshots never land in the current
working directory. Use '-o <path>' to choose the output file. Use '-o -' to
write the raw JPEG bytes to stdout (logs go to stderr, so the output can be piped
directly into an image consumer). Because '-o -' emits binary data it cannot be
combined with a structured format (--json/--format json|yaml|plaintext); write
to a file in that case. Use --frames N to capture N consecutive frames; with
N > 1 each frame is written as <name>-001.jpg, <name>-002.jpg, ...

--region X1,Y1,X2,Y2 crops the frame (useful for reading a small button), and
--scale N upscales by a nearest-neighbor factor. JSON/YAML output carries the
region and scale so a coordinate in the crop maps back to source pixels:
source_x = region.x1 + crop_x / scale.

--until-stable captures until two consecutive frames are byte-identical (waiting
for the UI to settle instead of sleeping); it honours --stable-interval and
--stable-timeout. Use --keep-alive to hold the stream socket open (in a separate
terminal) so that external snapshot fetches keep working; press Ctrl-C to stop.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli screenshot
  kvm-cli screenshot -o /tmp/screen.jpg
  kvm-cli screenshot -o - > /tmp/screen.jpg
  kvm-cli screenshot --region 1440,150,1820,360 --scale 2 -o /tmp/btn.jpg
  kvm-cli screenshot --until-stable
  kvm-cli screenshot --frames 5 -o /tmp/frame.jpg
  kvm-cli screenshot --keep-alive`,
	RunE: runScreenshot,
}

func runScreenshot(cmd *cobra.Command, args []string) error {
	var region *elements.Region
	if s := strings.TrimSpace(flagShotRegion); s != "" {
		r, err := elements.ParseRegion(s)
		if err != nil {
			return output.NewCodedError("USAGE", err.Error())
		}
		region = &r
	}
	if vncTargetConfigured() {
		return runScreenshotVNC(cmd, region)
	}
	return shotCapture(cmd, shotOptions{
		Output:         flagShotOutput,
		Frames:         flagShotFrames,
		KeepAlive:      flagShotKeepAlive,
		Region:         region,
		Scale:          flagShotScale,
		UntilStable:    flagShotUntilStable,
		StableInterval: flagShotStableInterval,
		StableTimeout:  flagShotStableTimeout,
	})
}

// runScreenshotVNC captures a frame over VNC (PNG) with optional --region/--scale.
func runScreenshotVNC(cmd *cobra.Command, region *elements.Region) error {
	if flagShotFrames > 1 {
		return output.NewCodedError("USAGE", "--frames is not supported with --vnc")
	}
	if flagShotKeepAlive {
		return output.NewCodedError("USAGE", "--keep-alive is not supported with --vnc")
	}
	opts := GetOutputOptions()
	out := strings.TrimSpace(flagShotOutput)
	if out == "" {
		dir, err := ScratchDir()
		if err != nil {
			return err
		}
		out, err = scratch.PathIn(dir, "vnc-screenshot", ".png")
		if err != nil {
			return err
		}
	}
	toStdout := out == "-"
	if toStdout && opts.Mode != output.ModeTable {
		return output.NewCodedError("USAGE",
			"-o - writes raw PNG to stdout and cannot be combined with a structured format")
	}

	png, w, h, err := VNCScreenshot(cmd.Context())
	if err != nil {
		return err
	}
	sourceW, sourceH := w, h
	fr, rm, scale, err := shotApplyCrop(shotOptions{Region: region, Scale: flagShotScale},
		shotFrame{Data: png, Width: w, Height: h})
	if err != nil {
		return err
	}
	png, w, h = fr.Data, fr.Width, fr.Height

	if toStdout {
		if _, werr := os.Stdout.Write(png); werr != nil {
			return fmt.Errorf("write png to stdout: %w", werr)
		}
		fmt.Fprintf(os.Stderr, "Wrote %d bytes of PNG to stdout\n", len(png))
		return nil
	}
	if err := os.WriteFile(out, png, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	fmt.Fprintf(os.Stderr, "Saved %s (%d bytes, %dx%d)\n", out, len(png), w, h)
	info := screenshotInfo{
		Path: out, Bytes: len(png), Width: w, Height: h,
		SourceWidth: sourceW, SourceHeight: sourceH,
		Region: rm, Scale: scale, Frames: 1,
	}
	if opts.Mode != output.ModeTable {
		return output.Render(output.TableData{}, []screenshotInfo{info}, opts)
	}
	return nil
}

// regionMeta is the JSON shape of a capture region.
type regionMeta struct {
	X1 float64 `json:"x1" yaml:"x1"`
	Y1 float64 `json:"y1" yaml:"y1"`
	X2 float64 `json:"x2" yaml:"x2"`
	Y2 float64 `json:"y2" yaml:"y2"`
}

// screenshotInfo is the structured metadata emitted for a completed capture.
type screenshotInfo struct {
	Path         string      `json:"path" yaml:"path"`
	Bytes        int         `json:"bytes" yaml:"bytes"`
	Width        int         `json:"width,omitempty" yaml:"width,omitempty"`
	Height       int         `json:"height,omitempty" yaml:"height,omitempty"`
	SourceWidth  int         `json:"source_width,omitempty" yaml:"source_width,omitempty"`
	SourceHeight int         `json:"source_height,omitempty" yaml:"source_height,omitempty"`
	Region       *regionMeta `json:"region,omitempty" yaml:"region,omitempty"`
	Scale        float64     `json:"scale,omitempty" yaml:"scale,omitempty"`
	Frames       int         `json:"frames" yaml:"frames"`
}

// shotFrame is one captured JPEG frame plus its pixel dimensions when known.
type shotFrame struct {
	Data   []byte
	Width  int
	Height int
}

// defaultShotPath mints a unique timestamped screenshot path under the effective
// scratch directory. It is the default for 'screenshot' and 'streamer snapshot',
// so a bare invocation never writes into the current working directory.
func defaultShotPath() (string, error) {
	dir, err := ScratchDir()
	if err != nil {
		return "", err
	}
	return scratch.PathIn(dir, "kvm-screenshot", ".jpg")
}

// shotCapture is the shared implementation behind 'screenshot' and
// 'streamer snapshot'.
func shotCapture(cmd *cobra.Command, o shotOptions) error {
	if o.Frames < 1 {
		return output.NewCodedError("USAGE", "--frames must be >= 1")
	}
	if o.UntilStable && o.Frames > 1 {
		return output.NewCodedError("USAGE", "--until-stable cannot be combined with --frames > 1")
	}
	outputPath := strings.TrimSpace(o.Output)
	if outputPath == "" {
		path, err := defaultShotPath()
		if err != nil {
			return err
		}
		outputPath = path
	}
	opts := GetOutputOptions()
	toStdout := outputPath == "-"
	if toStdout && o.Frames != 1 {
		return output.NewCodedError("USAGE", "-o - cannot be combined with --frames > 1")
	}
	if toStdout && opts.Mode != output.ModeTable {
		return output.NewCodedError("USAGE",
			"-o - writes raw JPEG to stdout and cannot be combined with a structured format (--json/--format)")
	}

	var captured []shotFrame
	if o.UntilStable {
		fr, _, err := shotGrabStable(cmd, o.StableInterval, o.StableTimeout)
		if err != nil {
			return err
		}
		captured = []shotFrame{fr}
	} else {
		frames, _, err := shotGrabFrames(cmd, o.Frames, o.KeepAlive)
		if o.KeepAlive {
			return err
		}
		if err != nil {
			return err
		}
		captured = frames
	}

	infos := make([]screenshotInfo, 0, len(captured))
	for i, fr := range captured {
		sourceW, sourceH := fr.Width, fr.Height
		fr, rm, cropScale, cerr := shotApplyCrop(o, fr)
		if cerr != nil {
			return cerr
		}

		if toStdout {
			if _, werr := os.Stdout.Write(fr.Data); werr != nil {
				return fmt.Errorf("write snapshot to stdout: %w", werr)
			}
			fmt.Fprintf(os.Stderr, "Wrote %d bytes of JPEG to stdout\n", len(fr.Data))
			continue
		}

		path := outputPath
		if o.Frames > 1 {
			path = numberedPath(outputPath, i+1)
		}
		if err := os.WriteFile(path, fr.Data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		info := screenshotInfo{
			Path: path, Bytes: len(fr.Data),
			Width: fr.Width, Height: fr.Height,
			SourceWidth: sourceW, SourceHeight: sourceH,
			Region: rm, Scale: cropScale,
			Frames: 1,
		}
		infos = append(infos, info)
		fmt.Fprintf(os.Stderr, "Saved %s (%d bytes, %dx%d)\n", path, info.Bytes, info.Width, info.Height)
	}

	if toStdout {
		return nil
	}

	if opts.Mode != output.ModeTable {
		return output.Render(output.TableData{}, infos, opts)
	}
	return nil
}

// shotApplyCrop applies --region/--scale to a captured frame, returning the
// cropped frame, the region metadata (nil when no crop was applied) and the
// effective scale. It is a no-op when neither region nor a non-unit scale is set.
func shotApplyCrop(o shotOptions, fr shotFrame) (shotFrame, *regionMeta, float64, error) {
	if o.Region == nil && (o.Scale <= 0 || o.Scale == 1) {
		return fr, nil, 0, nil
	}
	r := elements.Region{X1: 0, Y1: 0, X2: float64(fr.Width), Y2: float64(fr.Height)}
	if o.Region != nil {
		r = *o.Region
	}
	scale := o.Scale
	if scale <= 0 {
		scale = 1
	}
	data, w, h, err := crop.CropScale(fr.Data, r, scale)
	if err != nil {
		return fr, nil, 0, err
	}
	fr.Data, fr.Width, fr.Height = data, w, h
	return fr, &regionMeta{X1: r.X1, Y1: r.Y1, X2: r.X2, Y2: r.Y2}, scale, nil
}

// shotOpenStream authenticates, opens a short-lived stream WebSocket and waits
// (best effort) for the streamer to report a resolution. Callers must Close the
// returned stream.
func shotOpenStream(cmd *cobra.Command) (*api.Client, *ws.Client, ws.Resolution, error) {
	client, err := NewClient(cmd)
	if err != nil {
		return nil, nil, ws.Resolution{}, err
	}
	stream, err := ws.Connect(client.BaseURL(), client.Token(), hidWSOptions())
	if err != nil {
		return nil, nil, ws.Resolution{}, fmt.Errorf("open stream socket: %w", err)
	}
	res, resErr := stream.Resolution(8 * time.Second)
	if resErr != nil {
		DebugLog("streamer resolution not reported yet: %v", resErr)
	} else {
		if settled, sErr := shotSettleResolution(stream, 3*time.Second); sErr == nil {
			res = settled
		}
		DebugLog("streamer resolution %dx%d", res.Width, res.Height)
	}
	return client, stream, res, nil
}

// shotGrabFrames connects a short-lived stream socket, waits for the streamer to
// report a resolution, and fetches `frames` JPEG frames. When keepAlive is true
// it instead holds the socket open until interrupted (returning no frames). It
// is the silent core shared by 'screenshot'/'streamer snapshot' and the cua
// commands: it writes no files and prints nothing to stdout.
func shotGrabFrames(cmd *cobra.Command, frames int, keepAlive bool) ([]shotFrame, ws.Resolution, error) {
	client, stream, res, err := shotOpenStream(cmd)
	if err != nil {
		return nil, ws.Resolution{}, err
	}
	defer func() { _ = stream.Close() }()

	if keepAlive {
		return nil, res, shotKeepAlive(cmd, stream)
	}

	// Honour the global --timeout for the snapshot GET rather than a hardcoded
	// default, so a slow network can be given more (or less) time.
	timeout := flagTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	out := make([]shotFrame, 0, frames)
	for i := 0; i < frames; i++ {
		data, ctype, err := shotFetch(client.BaseURL(), client.Token(), 25, 300*time.Millisecond, timeout)
		if err != nil {
			return nil, res, fmt.Errorf("fetch snapshot: %w", err)
		}
		if !looksLikeJPEG(data) {
			return nil, res, fmt.Errorf("snapshot is not a JPEG (content-type=%q, %d bytes)", ctype, len(data))
		}
		fr := shotFrame{Data: data, Width: res.Width, Height: res.Height}
		if w, h, ok := jpegDimensions(data); ok {
			fr.Width, fr.Height = w, h
		}
		out = append(out, fr)
	}
	return out, res, nil
}

// shotGrabStable fetches frames over a single stream socket until two
// consecutive frames are byte-identical (the UI has settled) or the timeout
// elapses, in which case the most recent frame is returned. It removes the
// sleep-and-recapture loop from agents.
func shotGrabStable(cmd *cobra.Command, interval, timeout time.Duration) (shotFrame, ws.Resolution, error) {
	if interval <= 0 {
		interval = 400 * time.Millisecond
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client, stream, res, err := shotOpenStream(cmd)
	if err != nil {
		return shotFrame{}, ws.Resolution{}, err
	}
	defer func() { _ = stream.Close() }()

	reqTimeout := flagTimeout
	if reqTimeout <= 0 {
		reqTimeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var prev []byte
	var last shotFrame
	for {
		data, _, ferr := shotFetch(client.BaseURL(), client.Token(), 20, 250*time.Millisecond, reqTimeout)
		if ferr != nil {
			return shotFrame{}, res, fmt.Errorf("fetch snapshot: %w", ferr)
		}
		last = shotFrame{Data: data, Width: res.Width, Height: res.Height}
		if w, h, ok := jpegDimensions(data); ok {
			last.Width, last.Height = w, h
		}
		if prev != nil && bytes.Equal(prev, data) {
			return last, res, nil
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "stable-timeout reached; using the most recent frame")
			return last, res, nil
		}
		prev = data
		time.Sleep(interval)
	}
}

// shotSettleResolution waits until the streamer reports a stable non-zero
// resolution (two identical reads) while the H.264 pipeline is online. The
// encoder first advertises a provisional resolution and later the negotiated
// one, so this avoids reporting a stale value.
func shotSettleResolution(stream *ws.Client, timeout time.Duration) (ws.Resolution, error) {
	deadline := time.Now().Add(timeout)
	prev := ws.Resolution{}
	var last ws.Resolution
	for {
		st := stream.ReadState()
		cur := st.Streamer.Resolution
		if cur.Width > 0 {
			last = cur
		}
		if cur.Width > 0 && cur == prev && st.Streamer.Online {
			return cur, nil
		}
		if time.Now().After(deadline) {
			if last.Width > 0 {
				return last, nil
			}
			return cur, fmt.Errorf("no stable resolution within %s", timeout)
		}
		prev = cur
		time.Sleep(350 * time.Millisecond)
	}
}

// shotKeepAlive holds the stream socket open until the process is interrupted.
func shotKeepAlive(cmd *cobra.Command, stream *ws.Client) error {
	baseURL := stream.BaseURL()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(os.Stderr, "Stream socket held open; press Ctrl-C to stop.")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nClosing stream socket.")
			return nil
		case <-stream.Done():
			return fmt.Errorf("stream socket closed unexpectedly: %w", stream.Err())
		case <-ticker.C:
			state := stream.ReadState()
			fmt.Fprintf(os.Stderr, "stream alive: resolution=%dx%d jpeg_clients=%v (base %s)\n",
				state.Streamer.Resolution.Width, state.Streamer.Resolution.Height,
				state.Streamer.JPEGClients, redact.URL(baseURL))
		}
	}
}

// shotFetch retrieves a snapshot, retrying while the streamer reports the
// streamer-not-running 503. It returns the JPEG bytes and Content-Type. The
// per-request timeout comes from the global --timeout flag (F13).
func shotFetch(baseURL, token string, attempts int, wait, timeout time.Duration) ([]byte, string, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(wait)
		}
		data, ctype, status, err := shotFetchOnce(baseURL, token, timeout)
		if err == nil {
			return data, ctype, nil
		}
		lastErr = err
		if status != http.StatusServiceUnavailable && status != http.StatusTooManyRequests {
			return nil, "", err
		}
		DebugLog("snapshot attempt %d/%d: HTTP %d (streamer warming up)", attempt+1, attempts, status)
	}
	return nil, "", fmt.Errorf("snapshot unavailable after %d attempts: %w", attempts, lastErr)
}

// shotFetchOnce performs a single raw snapshot GET without the api.Client's
// automatic 5xx retries, so retry timing stays under our control.
func shotFetchOnce(baseURL, token string, timeout time.Duration) ([]byte, string, int, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/streamer/snapshot", nil)
	if err != nil {
		// The composed URL may carry userinfo; url.Error embeds the raw URL.
		return nil, "", 0, redact.Error(err)
	}
	req.Header.Set("token", token)
	req.Header.Set("Accept", "image/jpeg")

	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: hidTLSInsecure()}}, //nolint:gosec
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// http.Client.Do returns a *url.Error embedding the full URL (which may
		// carry userinfo); redact before it reaches the user.
		return nil, "", 0, redact.Error(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", resp.StatusCode, fmt.Errorf("read snapshot: %w", err)
	}
	ctype := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ctype, resp.StatusCode, fmt.Errorf("snapshot: HTTP %d: %s", resp.StatusCode, truncateBody(body))
	}
	return body, ctype, resp.StatusCode, nil
}

func truncateBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// looksLikeJPEG reports whether data begins with the JPEG SOI marker.
func looksLikeJPEG(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF
}

// jpegDimensions extracts the image dimensions from the JPEG SOF marker. It
// returns ok=false for a payload without a recognisable SOF segment.
func jpegDimensions(data []byte) (width, height int, ok bool) {
	if !looksLikeJPEG(data) {
		return 0, 0, false
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		i += 2
		// Standalone markers have no length payload.
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			continue
		}
		if i+2 > len(data) {
			return 0, 0, false
		}
		segLen := int(data[i])<<8 | int(data[i+1])
		if segLen < 2 || i+segLen > len(data) {
			return 0, 0, false
		}
		// SOF0..SOF15 excluding DHT (C4), JPG (C8) and DAC (CC).
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if i+7 > len(data) {
				return 0, 0, false
			}
			height = int(data[i+3])<<8 | int(data[i+4])
			width = int(data[i+5])<<8 | int(data[i+6])
			return width, height, width > 0 && height > 0
		}
		i += segLen
	}
	return 0, 0, false
}

// numberedPath inserts a zero-padded frame index before the extension.
func numberedPath(path string, n int) string {
	ext := ""
	stem := path
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		stem = path[:i]
		ext = path[i:]
	}
	return fmt.Sprintf("%s-%03d%s", stem, n, ext)
}

func init() {
	screenshotCmd.Flags().StringVarP(&flagShotOutput, "output", "o", "", "Output file (default: scratch dir), or - for stdout")
	screenshotCmd.Flags().IntVar(&flagShotFrames, "frames", 1, "Number of consecutive frames to capture")
	screenshotCmd.Flags().BoolVar(&flagShotKeepAlive, "keep-alive", false, "Hold the stream socket open until interrupted")
	screenshotCmd.Flags().StringVar(&flagShotRegion, "region", "", "Crop to X1,Y1,X2,Y2 source pixels")
	screenshotCmd.Flags().Float64Var(&flagShotScale, "scale", 0, "Upscale factor (nearest-neighbor; default 1)")
	screenshotCmd.Flags().BoolVar(&flagShotUntilStable, "until-stable", false, "Capture until two consecutive frames are identical")
	registerVNCFlags(screenshotCmd)
	screenshotCmd.Flags().DurationVar(&flagShotStableInterval, "stable-interval", 400*time.Millisecond, "Poll interval for --until-stable")
	screenshotCmd.Flags().DurationVar(&flagShotStableTimeout, "stable-timeout", 15*time.Second, "Give up on --until-stable after this long")
	rootCmd.AddCommand(screenshotCmd)
}
