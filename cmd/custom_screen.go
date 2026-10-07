package cmd

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// screenCmd groups the custom-screen (on-device display) endpoints. It is named
// "screen" for brevity with aliases for the underlying API resource name.
var screenCmd = &cobra.Command{
	Use:     "screen",
	Aliases: []string{"custom-screen", "custom_screen"},
	Short:   "Configure the device's custom screen (background, clock, mode)",
	Long: `Configure the remote KVM's custom screen: the wallpaper shown on the device's
display, the clock mode, and the date/time formats.

Background changes are destructive to the current wallpaper and require --yes.
Some firmware builds do not implement this API; those commands report that the
feature is not supported on this device rather than printing a raw error.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli screen status
  kvm-cli screen background --output wallpaper.png
  kvm-cli screen set-background ./wallpaper.png --yes
  kvm-cli screen set-mode clock_only
  kvm-cli screen set-time-format 24h`,
}

var screenStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show custom screen settings (GET /api/custom_screen/status)",
	Long:    "Show the current screen mode, date format, and time format. Reports a clear message when the device does not implement the endpoint.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli screen status\n  kvm-cli screen status --json",
	RunE:    runScreenStatus,
}

var screenBackgroundCmd = &cobra.Command{
	Use:     "background",
	Short:   "Fetch the current background image (GET /api/custom_screen/background)",
	Long:    "Fetch the current custom-screen background. The device returns it base64-encoded; use --output to decode it to a file.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli screen background\n  kvm-cli screen background --output wallpaper.png",
	RunE:    runScreenBackground,
}

var screenSetBackgroundCmd = &cobra.Command{
	Use:   "set-background FILE",
	Short: "Upload a background image (POST /api/custom_screen/update_background) — DESTRUCTIVE",
	Long: `Upload <file> as the custom-screen background, replacing the current one.

Destructive: requires --yes. Without --yes the command prints what it would do
and exits non-zero without uploading.`,
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli screen set-background ./wallpaper.png --yes",
	RunE:    runScreenSetBackground,
}

var screenDeleteBackgroundCmd = &cobra.Command{
	Use:     "delete-background",
	Short:   "Delete the custom background (POST /api/custom_screen/delete_background) — DESTRUCTIVE",
	Long:    "Remove the current custom-screen background. Destructive: requires --yes.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli screen delete-background --yes",
	RunE:    runScreenDeleteBackground,
}

var screenSetDateCmd = &cobra.Command{
	Use:     "set-date-format FORMAT",
	Short:   "Set the date format (POST /api/custom_screen/update_date_format)",
	Long:    "Set the screen's date format. Known values: locale, mm_dd_yyyy, dd_mm_yyyy, yyyy_mm_dd.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli screen set-date-format yyyy_mm_dd",
	RunE:    runScreenSetDateFormat,
}

var screenSetTimeCmd = &cobra.Command{
	Use:     "set-time-format FORMAT",
	Short:   "Set the time format (POST /api/custom_screen/update_time_format)",
	Long:    "Set the screen's time format. Known values: 24h, 12h.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli screen set-time-format 24h",
	RunE:    runScreenSetTimeFormat,
}

var screenSetModeCmd = &cobra.Command{
	Use:     "set-mode MODE",
	Short:   "Set the screen mode (POST /api/custom_screen/update_screen_mode)",
	Long:    "Set the screen mode. Known values: default (world clock), clock_only, wallpaper_only.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli screen set-mode clock_only",
	RunE:    runScreenSetMode,
}

var (
	screenSetBackgroundYes bool
	screenDeleteBgYes      bool
	screenBackgroundOutput string
)

func init() {
	vmRegisterConfirm(screenSetBackgroundCmd, &screenSetBackgroundYes, "Confirm replacing the current background (required to proceed)")
	vmRegisterConfirm(screenDeleteBackgroundCmd, &screenDeleteBgYes, "Confirm deleting the current background (required to proceed)")
	screenBackgroundCmd.Flags().StringVarP(&screenBackgroundOutput, "output", "o", "", "Decode the background image to this file")

	MarkWrite(screenSetBackgroundCmd)
	MarkWrite(screenDeleteBackgroundCmd)
	MarkWrite(screenSetDateCmd)
	MarkWrite(screenSetTimeCmd)
	MarkWrite(screenSetModeCmd)

	screenCmd.AddCommand(
		screenStatusCmd,
		screenBackgroundCmd,
		screenSetBackgroundCmd,
		screenDeleteBackgroundCmd,
		screenSetDateCmd,
		screenSetTimeCmd,
		screenSetModeCmd,
	)
	rootCmd.AddCommand(screenCmd)
}

func runScreenStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/custom_screen/status", &result); err != nil {
		return screenAPIError(err, "/api/custom_screen/status")
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    vmFlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runScreenBackground(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/custom_screen/background", &result); err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "NotFoundError" {
			// No background has been set: a normal empty state, not a failure.
			return renderScreenBackground(nil, screenBackgroundOutput)
		}
		return screenAPIError(err, "/api/custom_screen/background")
	}

	return renderScreenBackground(result, screenBackgroundOutput)
}

// renderScreenBackground renders the {format,data} background payload, decoding
// the base64 image to outputPath when one is given.
func renderScreenBackground(result map[string]any, outputPath string) error {
	format := vmValueString(result["format"])
	encoded := vmValueString(result["data"])

	decoded, decodeErr := decodeBackground(encoded)
	written := ""
	if outputPath != "" {
		if encoded == "" && len(decoded) == 0 {
			return output.NewCodedError("NOT_FOUND", "no background image is set on the device; nothing to write")
		}
		if decodeErr != nil {
			return output.WrapCodedError("ERROR", decodeErr, fmt.Sprintf("decode background: %v", decodeErr))
		}
		if err := os.WriteFile(outputPath, decoded, 0o644); err != nil {
			return output.WrapCodedError("ERROR", err, fmt.Sprintf("write %s: %v", outputPath, err))
		}
		written = outputPath
	}

	data := map[string]any{
		"format":    format,
		"bytes":     len(decoded),
		"has_image": len(decoded) > 0,
	}
	if written != "" {
		data["output"] = written
	}

	td := output.TableData{Headers: []string{"FORMAT", "BYTES", "OUTPUT"}}
	if len(decoded) == 0 {
		td.Footer = "no background image set"
	} else {
		td.Rows = append(td.Rows, []string{format, vmValueString(len(decoded)), written})
	}
	return output.Render(td, data, GetOutputOptions())
}

// decodeBackground base64-decodes the device's background payload, tolerating a
// "data:<mime>;base64,<data>" data-URI prefix.
func decodeBackground(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, nil
	}
	if i := strings.Index(encoded, ";base64,"); i >= 0 {
		encoded = encoded[i+len(";base64,"):]
	}
	return base64.StdEncoding.DecodeString(encoded)
}

func runScreenSetBackground(cmd *cobra.Command, args []string) error {
	file := args[0]
	data, err := os.ReadFile(file)
	if err != nil {
		return output.WrapCodedError("USAGE", err, fmt.Sprintf("read %s: %v", file, err))
	}
	if err := vmRequireYes(screenSetBackgroundYes, fmt.Sprintf("replace the screen background with %q (%s)", file, vmFormatBytes(float64(len(data))))); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	contentType, body, err := vmMultipartFile("file", filepath.Base(file), data)
	if err != nil {
		return output.WrapCodedError("ERROR", err, fmt.Sprintf("build multipart upload: %v", err))
	}

	var result map[string]any
	if err := vmRawPost(client, "/api/custom_screen/update_background", contentType, body, vmUploadTimeout(cmd), &result); err != nil {
		return screenAPIError(err, "/api/custom_screen/update_background")
	}
	return vmRenderAction("updated custom screen background", result)
}

func runScreenDeleteBackground(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(screenDeleteBgYes, "delete the custom screen background"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/custom_screen/delete_background", nil, &result); err != nil {
		return screenAPIError(err, "/api/custom_screen/delete_background")
	}
	return vmRenderAction("deleted custom screen background", result)
}

func runScreenSetDateFormat(cmd *cobra.Command, args []string) error {
	return screenSetStringParam(cmd, "update_date_format", "format_str", args[0], "date format")
}

func runScreenSetTimeFormat(cmd *cobra.Command, args []string) error {
	return screenSetStringParam(cmd, "update_time_format", "format_str", args[0], "time format")
}

func runScreenSetMode(cmd *cobra.Command, args []string) error {
	return screenSetStringParam(cmd, "update_screen_mode", "mode_str", args[0], "screen mode")
}

// screenSetStringParam posts a single string parameter to a custom_screen
// endpoint.
func screenSetStringParam(cmd *cobra.Command, endpoint, param, value, label string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return output.NewCodedError("USAGE", "a "+label+" value is required")
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/custom_screen/"+endpoint+"?"+url.Values{param: {value}}.Encode(), nil, &result); err != nil {
		return screenAPIError(err, "/api/custom_screen/"+endpoint)
	}
	return vmRenderAction(fmt.Sprintf("set %s to %q", label, value), result)
}

// screenAPIError converts failures that mean the custom-screen feature is
// unavailable on this firmware into a clear NOT_SUPPORTED error. Any other
// error is passed through vmDeviceError.
func screenAPIError(err error, endpoint string) error {
	if err == nil {
		return nil
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && (apiErr.IsNotFound() || apiErr.Code == "BadRequestError") {
		msg := strings.TrimSpace(apiErr.Message)
		if msg == "" {
			msg = apiErr.Code
		}
		return output.NewCodedError("NOT_SUPPORTED",
			fmt.Sprintf("custom screen is not supported on this device (%s): %s", endpoint, msg))
	}
	return vmDeviceError(err)
}

// vmMultipartFile builds a multipart/form-data body with a single file field.
func vmMultipartFile(field, filename string, data []byte) (string, []byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		return "", nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return "", nil, err
	}
	if err := w.Close(); err != nil {
		return "", nil, err
	}
	return w.FormDataContentType(), buf.Bytes(), nil
}
