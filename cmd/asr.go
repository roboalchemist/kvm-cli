package cmd

import (
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// asrCmd groups automatic speech recognition (ASR) control. The device exposes
// /api/asr, /api/asr/start, and /api/asr/stop; the ASR feature is only present
// on some firmware/hardware, so commands report a clear "not available" message
// when the endpoints are missing.
var asrCmd = &cobra.Command{
	Use:   "asr",
	Short: "Inspect and control automatic speech recognition",
	Long: `Inspect and control the remote KVM's automatic speech recognition (ASR).

'status' reports whether ASR is running; 'start' and 'stop' toggle it. These
are benign toggles (they only affect audio transcription) so no --yes is
required. ASR is not available on every device; when it is absent the commands
report that clearly instead of a raw error.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli asr status
  kvm-cli asr start
  kvm-cli asr stop`,
}

var asrStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show ASR state (GET /api/asr)",
	Long:    "Show whether automatic speech recognition is currently running. Reports a clear message when ASR is unavailable on this device.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli asr status\n  kvm-cli asr status --json",
	RunE:    runASRStatus,
}

var asrStartCmd = &cobra.Command{
	Use:     "start",
	Short:   "Start ASR (POST /api/asr/start)",
	Long:    "Start automatic speech recognition. Benign: no --yes required. Reports a clear message when ASR is unavailable on this device.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli asr start",
	RunE:    runASRStart,
}

var asrStopCmd = &cobra.Command{
	Use:     "stop",
	Short:   "Stop ASR (POST /api/asr/stop)",
	Long:    "Stop automatic speech recognition. Benign: no --yes required. Reports a clear message when ASR is unavailable on this device.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli asr stop",
	RunE:    runASRStop,
}

func init() {
	MarkWrite(asrStartCmd)
	MarkWrite(asrStopCmd)

	asrCmd.AddCommand(asrStatusCmd, asrStartCmd, asrStopCmd)
	rootCmd.AddCommand(asrCmd)
}

func runASRStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/asr", &result); err != nil {
		return k10Unsupported(err, "automatic speech recognition (ASR)", "/api/asr")
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runASRStart(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/asr/start", nil, &result); err != nil {
		return k10Unsupported(err, "automatic speech recognition (ASR)", "/api/asr/start")
	}
	return vmRenderAction("started ASR", result)
}

func runASRStop(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/asr/stop", nil, &result); err != nil {
		return k10Unsupported(err, "automatic speech recognition (ASR)", "/api/asr/stop")
	}
	return vmRenderAction("stopped ASR", result)
}
