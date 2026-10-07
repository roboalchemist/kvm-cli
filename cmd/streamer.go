package cmd

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// streamer flags.
var (
	flagStreamerSnapOutput    string
	flagStreamerSnapFrames    int
	flagStreamerSnapKeepAlive bool

	streamerQuality     int
	streamerFPS         int
	streamerBitrate     int
	streamerGop         int
	streamerVencMode    string
	streamerZeroDelay   bool
	streamerVideoFormat int
)

// streamerStatus mirrors GET /api/streamer.
type streamerStatus struct {
	Features map[string]bool `json:"features" yaml:"features"`
	Limits   map[string]any  `json:"limits" yaml:"limits"`
	Params   struct {
		DesiredFPS  int    `json:"desired_fps" yaml:"desired_fps"`
		Quality     int    `json:"quality" yaml:"quality"`
		H264Bitrate int    `json:"h264_bitrate" yaml:"h264_bitrate"`
		H264Gop     int    `json:"h264_gop" yaml:"h264_gop"`
		VencMode    string `json:"venc_mode" yaml:"venc_mode"`
		ZeroDelay   bool   `json:"zero_delay" yaml:"zero_delay"`
		VideoFormat int    `json:"video_format" yaml:"video_format"`
	} `json:"params" yaml:"params"`
	Snapshot map[string]any `json:"snapshot" yaml:"snapshot"`
	Streamer *struct {
		Encoder struct {
			Type    string `json:"type" yaml:"type"`
			Quality int    `json:"quality" yaml:"quality"`
		} `json:"encoder" yaml:"encoder"`
		HDMI struct {
			Signal bool `json:"signal" yaml:"signal"`
		} `json:"hdmi" yaml:"hdmi"`
		H264 struct {
			Bitrate int  `json:"bitrate" yaml:"bitrate"`
			Gop     int  `json:"gop" yaml:"gop"`
			Online  bool `json:"online" yaml:"online"`
			FPS     int  `json:"fps" yaml:"fps"`
		} `json:"h264" yaml:"h264"`
		Sinks struct {
			JPEG struct {
				HasClients bool `json:"has_clients" yaml:"has_clients"`
			} `json:"jpeg" yaml:"jpeg"`
			H264 struct {
				HasClients bool `json:"has_clients" yaml:"has_clients"`
			} `json:"h264" yaml:"h264"`
		} `json:"sinks" yaml:"sinks"`
		Source struct {
			Resolution struct {
				Width  int `json:"width" yaml:"width"`
				Height int `json:"height" yaml:"height"`
			} `json:"resolution" yaml:"resolution"`
			RealResolution string `json:"real_resolution" yaml:"real_resolution"`
		} `json:"source" yaml:"source"`
	} `json:"streamer" yaml:"streamer"`
}

var streamerCmd = &cobra.Command{
	Use:   "streamer",
	Short: "Inspect and configure the video streamer",
	Long: `Inspect the KVM video streamer and capture snapshots.

  streamer status       show streamer parameters and live state
  streamer snapshot     alias for 'kvm-cli screenshot'
  streamer set-params   change encoder parameters (quality, fps, bitrate, ...)

Snapshots are only produced while a streaming client is connected; the snapshot
subcommand (and 'kvm-cli screenshot') handles opening a temporary stream socket
automatically.`,
	Example: `  kvm-cli streamer status
  kvm-cli streamer snapshot -o /tmp/screen.jpg
  kvm-cli streamer set-params --quality 90 --fps 30`,
}

var streamerStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show the streamer state and parameters",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli streamer status\n  kvm-cli streamer status --json",
	RunE:    runStreamerStatus,
}

var streamerSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Capture a JPEG snapshot (alias for screenshot)",
	Long: `Capture a JPEG snapshot from the video stream.

This is an alias for 'kvm-cli screenshot' with the same output semantics: by
default it writes a unique, timestamped file under the scratch directory (the OS
temp directory unless --scratch-dir, $KVM_SCRATCH_DIR or config 'scratch_dir'
says otherwise) — never the current working directory — and '-o -' writes raw
JPEG bytes to stdout. Use --keep-alive to hold the stream socket open (in a
separate terminal) so external snapshot fetches keep working; press Ctrl-C to
stop.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli streamer snapshot
  kvm-cli streamer snapshot -o /tmp/screen.jpg
  kvm-cli streamer snapshot -o - > /tmp/screen.jpg
  kvm-cli streamer snapshot --keep-alive`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return shotCapture(cmd, shotOptions{
			Output:    flagStreamerSnapOutput,
			Frames:    flagStreamerSnapFrames,
			KeepAlive: flagStreamerSnapKeepAlive,
		})
	},
}

var streamerSetParamsCmd = &cobra.Command{
	Use:   "set-params",
	Short: "Change streamer encoder parameters",
	Long: `Change streamer encoder parameters via POST /api/streamer/set_params. Only the
flags you pass are sent.

  --quality       JPEG/encoder quality
  --fps           desired frames per second
  --h264-bitrate  H.264 bitrate (kbps)
  --h264-gop      H.264 GOP size
  --venc-mode     video encoder mode (normal|...)
  --zero-delay    enable/disable zero-delay encoding
  --video-format  video format selector`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli streamer set-params --quality 90
  kvm-cli streamer set-params --fps 30 --h264-bitrate 4000
  kvm-cli streamer set-params --zero-delay=false`,
	RunE: runStreamerSetParams,
}

func runStreamerStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return err
	}
	var st streamerStatus
	if err := client.Get("/api/streamer", &st); err != nil {
		return fmt.Errorf("fetch streamer state: %w", err)
	}

	rows := [][]string{
		{"quality", strconv.Itoa(st.Params.Quality)},
		{"desired_fps", strconv.Itoa(st.Params.DesiredFPS)},
		{"h264_bitrate", strconv.Itoa(st.Params.H264Bitrate)},
		{"h264_gop", strconv.Itoa(st.Params.H264Gop)},
		{"venc_mode", emptyDash(st.Params.VencMode)},
		{"zero_delay", strconv.FormatBool(st.Params.ZeroDelay)},
		{"video_format", strconv.Itoa(st.Params.VideoFormat)},
	}
	if st.Streamer == nil {
		rows = append(rows, []string{"streamer", "not running (no stream client)"})
	} else {
		rows = append(rows,
			[]string{"encoder", emptyDash(st.Streamer.Encoder.Type)},
			[]string{"hdmi signal", strconv.FormatBool(st.Streamer.HDMI.Signal)},
			[]string{"h264 online", strconv.FormatBool(st.Streamer.H264.Online)},
			[]string{"h264 fps", strconv.Itoa(st.Streamer.H264.FPS)},
			[]string{"jpeg clients", strconv.FormatBool(st.Streamer.Sinks.JPEG.HasClients)},
			[]string{"resolution", fmt.Sprintf("%dx%d", st.Streamer.Source.Resolution.Width, st.Streamer.Source.Resolution.Height)},
			[]string{"real resolution", emptyDash(st.Streamer.Source.RealResolution)},
		)
	}

	td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: rows}
	return output.Render(td, st, GetOutputOptions())
}

func runStreamerSetParams(cmd *cobra.Command, args []string) error {
	q := url.Values{}
	changed := map[string]any{}
	flags := cmd.Flags()
	if flags.Changed("quality") {
		q.Set("quality", strconv.Itoa(streamerQuality))
		changed["quality"] = streamerQuality
	}
	if flags.Changed("fps") {
		q.Set("desired_fps", strconv.Itoa(streamerFPS))
		changed["desired_fps"] = streamerFPS
	}
	if flags.Changed("h264-bitrate") {
		q.Set("h264_bitrate", strconv.Itoa(streamerBitrate))
		changed["h264_bitrate"] = streamerBitrate
	}
	if flags.Changed("h264-gop") {
		q.Set("h264_gop", strconv.Itoa(streamerGop))
		changed["h264_gop"] = streamerGop
	}
	if flags.Changed("venc-mode") {
		q.Set("venc_mode", streamerVencMode)
		changed["venc_mode"] = streamerVencMode
	}
	if flags.Changed("zero-delay") {
		q.Set("zero_delay", strconv.FormatBool(streamerZeroDelay))
		changed["zero_delay"] = streamerZeroDelay
	}
	if flags.Changed("video-format") {
		q.Set("video_format", strconv.Itoa(streamerVideoFormat))
		changed["video_format"] = streamerVideoFormat
	}

	if len(q) == 0 {
		return output.NewCodedError("USAGE", "no parameters given (see 'kvm-cli streamer set-params --help')")
	}

	client, err := NewClient(cmd)
	if err != nil {
		return err
	}
	if err := client.Post("/api/streamer/set_params?"+q.Encode(), nil, nil); err != nil {
		return fmt.Errorf("set streamer params: %w", err)
	}

	res := output.TableData{Headers: []string{"PARAM", "VALUE"}}
	for k, v := range changed {
		res.Rows = append(res.Rows, []string{k, fmt.Sprintf("%v", v)})
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Streamer parameters updated.")
	return output.Render(res, changed, GetOutputOptions())
}

func init() {
	streamerSnapshotCmd.Flags().StringVarP(&flagStreamerSnapOutput, "output", "o", "", "Output file (default: scratch dir), or - for stdout")
	streamerSnapshotCmd.Flags().IntVar(&flagStreamerSnapFrames, "frames", 1, "Number of consecutive frames to capture")
	streamerSnapshotCmd.Flags().BoolVar(&flagStreamerSnapKeepAlive, "keep-alive", false, "Hold the stream socket open until interrupted")

	streamerSetParamsCmd.Flags().IntVar(&streamerQuality, "quality", 0, "Encoder quality")
	streamerSetParamsCmd.Flags().IntVar(&streamerFPS, "fps", 0, "Desired frames per second")
	streamerSetParamsCmd.Flags().IntVar(&streamerBitrate, "h264-bitrate", 0, "H.264 bitrate (kbps)")
	streamerSetParamsCmd.Flags().IntVar(&streamerGop, "h264-gop", 0, "H.264 GOP size")
	streamerSetParamsCmd.Flags().StringVar(&streamerVencMode, "venc-mode", "", "Video encoder mode")
	streamerSetParamsCmd.Flags().BoolVar(&streamerZeroDelay, "zero-delay", false, "Enable zero-delay encoding")
	streamerSetParamsCmd.Flags().IntVar(&streamerVideoFormat, "video-format", 0, "Video format selector")

	MarkWrite(streamerSetParamsCmd)

	streamerCmd.AddCommand(streamerStatusCmd, streamerSnapshotCmd, streamerSetParamsCmd)
	rootCmd.AddCommand(streamerCmd)
}
