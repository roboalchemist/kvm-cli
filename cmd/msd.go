package cmd

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// msdCmd groups the virtual-media (Mass Storage Device) endpoints. The device
// presents an image from its internal storage to the target computer as a
// virtual USB drive or CD-ROM.
var msdCmd = &cobra.Command{
	Use:   "msd",
	Short: "Manage virtual media (Mass Storage Device)",
	Long: `Manage the remote KVM's virtual media (Mass Storage Device) emulation.

The device can present an image stored on the KVM to the target computer as a
virtual USB flash drive or CD-ROM. This command group covers inspecting the MSD
state, listing storage partitions, mounting/unmounting, uploading images, and
tuning the emulated drive parameters.

Destructive operations (format, remove, write, write-remote) refuse to run
unless --yes is supplied; without it they print a dry-run description and exit
non-zero without touching the device.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli msd status
  kvm-cli msd partitions
  kvm-cli msd connect /dev/disk/by-uuid/0717-B213
  kvm-cli msd disconnect
  kvm-cli msd write ./ubuntu.iso --yes
  kvm-cli msd write-remote https://example.com/win11.iso --yes`,
}

var msdStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show MSD state (GET /api/msd)",
	Long:    "Show the virtual-media state: whether it is enabled and online, whether the drive is connected, which image is mounted, and storage free/used space.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli msd status\n  kvm-cli msd status --json",
	RunE:    runMSDStatus,
}

var msdPartitionsCmd = &cobra.Command{
	Use:     "partitions",
	Short:   "List storage partitions (GET /api/msd/partition_show)",
	Long:    "List the device storage partitions available to the virtual media, including filesystem, size, label, UUID, and which one is current.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli msd partitions\n  kvm-cli msd partitions --json",
	RunE:    runMSDPartitions,
}

var msdConnectCmd = &cobra.Command{
	Use:     "connect IMAGE",
	Short:   "Mount a partition/image (POST /api/msd/partition_connect)",
	Long:    "Mount the partition or image at <image> (for example /dev/disk/by-uuid/XXXX) so the target sees it as removable media.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli msd connect /dev/disk/by-uuid/0717-B213",
	RunE:    runMSDConnect,
}

var msdDisconnectCmd = &cobra.Command{
	Use:     "disconnect",
	Short:   "Unmount the virtual media (POST /api/msd/partition_disconnect)",
	Long:    "Disconnect the currently mounted partition/image from the target.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli msd disconnect",
	RunE:    runMSDDisconnect,
}

var msdSetConnectedCmd = &cobra.Command{
	Use:     "set-connected TRUE|FALSE",
	Short:   "Attach/detach the virtual drive (POST /api/msd/set_connected)",
	Long:    "Attach (true) or detach (false) the virtual U-disk/CD-ROM from the target without changing the selected image.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli msd set-connected true\n  kvm-cli msd set-connected false",
	RunE:    runMSDSetConnected,
}

var msdFormatCmd = &cobra.Command{
	Use:   "format [PATH]",
	Short: "Format a partition (POST /api/msd/partition_format) — DESTRUCTIVE",
	Long: `Format the virtual-media partition, erasing its contents.

This is destructive and requires --yes. Without --yes the command prints what it
would do and exits non-zero. Optionally pass a partition path; when omitted the
device formats its current/default partition.`,
	Args:    cobra.MaximumNArgs(1),
	Example: "  kvm-cli msd format --yes\n  kvm-cli msd format /dev/mmcblk0p10 --yes",
	RunE:    runMSDFormat,
}

var msdRemoveCmd = &cobra.Command{
	Use:   "remove IMAGE",
	Short: "Delete an image (POST /api/msd/remove) — DESTRUCTIVE",
	Long:  "Delete the named image from the device's MSD storage. Destructive: requires --yes.",
	Args:  cobra.ExactArgs(1),
	Example: `  kvm-cli msd remove ubuntu.iso --yes
  kvm-cli msd remove ubuntu.iso            # dry run: prints what it would do`,
	RunE: runMSDRemove,
}

var msdWriteCmd = &cobra.Command{
	Use:   "write FILE",
	Short: "Upload an image (POST /api/msd/write) — DESTRUCTIVE",
	Long: `Upload a local disk image (typically an .iso) to the device's MSD storage.

The image name defaults to the base name of <file>; use --image to override it.
Destructive: requires --yes.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli msd write ./ubuntu.iso --yes
  kvm-cli msd write ./disk.img --image backup.img --yes
  kvm-cli msd write ./disk.img --prefix iso/ --yes`,
	RunE: runMSDWrite,
}

var msdWriteRemoteCmd = &cobra.Command{
	Use:   "write-remote URL",
	Short: "Ask the device to fetch an image by URL (POST /api/msd/write_remote) — DESTRUCTIVE",
	Long: `Ask the device to download an image from <url> directly into MSD storage.

Destructive: requires --yes. The filename is derived by the device from the URL.`,
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli msd write-remote https://example.com/win11.iso --yes",
	RunE:    runMSDWriteRemote,
}

var msdSetParamsCmd = &cobra.Command{
	Use:   "set-params KEY=VALUE...",
	Short: "Set MSD drive parameters (POST /api/msd/set_params)",
	Long: `Set one or more virtual-drive parameters, for example cdrom=true or
image=/dev/block/by-name/media.

Parameters are given as key=value pairs; boolean flags --cdrom and the --image
flag are convenient shortcuts. At least one parameter is required.`,
	Args: cobra.ArbitraryArgs,
	Example: `  kvm-cli msd set-params cdrom=true
  kvm-cli msd set-params --image /dev/block/by-name/media
  kvm-cli msd set-params --cdrom=false image=ubuntu.iso`,
	RunE: runMSDSetParams,
}

// Destructive-operation confirmations.
var (
	msdFormatYes      bool
	msdRemoveYes      bool
	msdWriteYes       bool
	msdWriteRemoteYes bool
)

// msd write-remote/write options.
var (
	msdWriteImage        string
	msdWritePrefix       string
	msdWriteRemotePrefix string
	msdRemoveIncomplete  bool
	msdRemoteIncomplete  bool
	msdSetParamsCDROM    bool
	msdSetParamsImage    string
)

func init() {
	vmRegisterConfirm(msdFormatCmd, &msdFormatYes, "Confirm this destructive format (required to proceed)")
	vmRegisterConfirm(msdRemoveCmd, &msdRemoveYes, "Confirm this destructive delete (required to proceed)")

	vmRegisterConfirm(msdWriteCmd, &msdWriteYes, "Confirm this destructive upload (required to proceed)")
	msdWriteCmd.Flags().StringVar(&msdWriteImage, "image", "", "Image name to store (default: base name of <file>)")
	msdWriteCmd.Flags().StringVar(&msdWritePrefix, "prefix", "", "Storage prefix/path for the uploaded image")
	msdWriteCmd.Flags().BoolVar(&msdRemoveIncomplete, "remove-incomplete", true, "Delete a partial upload before starting")

	vmRegisterConfirm(msdWriteRemoteCmd, &msdWriteRemoteYes, "Confirm this destructive download (required to proceed)")
	msdWriteRemoteCmd.Flags().StringVar(&msdWriteRemotePrefix, "prefix", "", "Storage prefix/path for the downloaded image")
	msdWriteRemoteCmd.Flags().BoolVar(&msdRemoteIncomplete, "remove-incomplete", true, "Delete a partial download before starting")

	msdSetParamsCmd.Flags().BoolVar(&msdSetParamsCDROM, "cdrom", false, "Set the drive as a CD-ROM (cdrom=true)")
	msdSetParamsCmd.Flags().StringVar(&msdSetParamsImage, "image", "", "Set the mounted image (image=<path>)")

	// Mutating MSD operations honour the central --dry-run enforcement.
	for _, c := range []*cobra.Command{
		msdConnectCmd,
		msdDisconnectCmd,
		msdSetConnectedCmd,
		msdFormatCmd,
		msdRemoveCmd,
		msdWriteCmd,
		msdWriteRemoteCmd,
		msdSetParamsCmd,
	} {
		MarkWrite(c)
	}

	msdCmd.AddCommand(
		msdStatusCmd,
		msdPartitionsCmd,
		msdConnectCmd,
		msdDisconnectCmd,
		msdSetConnectedCmd,
		msdFormatCmd,
		msdRemoveCmd,
		msdWriteCmd,
		msdWriteRemoteCmd,
		msdSetParamsCmd,
	)
	rootCmd.AddCommand(msdCmd)
}

func runMSDStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/msd", &result); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    vmFlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runMSDPartitions(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/msd/partition_show", &result); err != nil {
		return vmDeviceError(err)
	}

	devices, _ := vmAsMap(result["devices"])
	keys := vmSortedKeys(devices)

	td := output.TableData{Headers: []string{"PATH", "LABEL", "FILESYSTEM", "SIZE", "UUID", "CURRENT"}}
	for _, key := range keys {
		d, _ := vmAsMap(devices[key])
		path := key
		if p, ok := d["path"].(string); ok && p != "" {
			path = p
		}
		current := ""
		if b, ok := d["is_current"].(bool); ok && b {
			current = "yes"
		}
		td.Rows = append(td.Rows, []string{
			path,
			vmValueString(d["label"]),
			vmValueString(d["filesystem"]),
			vmFormatBytes(vmFloat(d["size"])),
			vmValueString(d["uuid"]),
			current,
		})
	}
	if len(td.Rows) == 0 {
		td.Footer = "no partitions reported"
	}
	return output.Render(td, result, GetOutputOptions())
}

func runMSDConnect(cmd *cobra.Command, args []string) error {
	image := args[0]

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	path := "/api/msd/partition_connect?" + url.Values{"path": {image}}.Encode()
	var result map[string]any
	if err := client.Post(path, nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("connected "+image, result)
}

func runMSDDisconnect(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/msd/partition_disconnect", nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("disconnected", result)
}

func runMSDSetConnected(cmd *cobra.Command, args []string) error {
	connected, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(args[0])))
	if err != nil {
		return output.NewCodedError("USAGE", fmt.Sprintf("invalid value %q (expected true or false)", args[0]))
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	path := "/api/msd/set_connected?" + url.Values{"connected": {strconv.FormatBool(connected)}}.Encode()
	var result map[string]any
	if err := client.Post(path, nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction(fmt.Sprintf("set-connected %t", connected), result)
}

func runMSDFormat(cmd *cobra.Command, args []string) error {
	target := ""
	if len(args) == 1 {
		target = args[0]
	}
	action := "format the MSD partition"
	if target != "" {
		action = fmt.Sprintf("format MSD partition %q", target)
	}
	if err := vmRequireYes(msdFormatYes, action); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	path := "/api/msd/partition_format"
	if target != "" {
		path += "?" + url.Values{"path": {target}}.Encode()
	}
	var result map[string]any
	if err := client.Post(path, nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction(action, result)
}

func runMSDRemove(cmd *cobra.Command, args []string) error {
	image := args[0]
	if err := vmRequireYes(msdRemoveYes, fmt.Sprintf("delete image %q from MSD storage", image)); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	path := "/api/msd/remove?" + url.Values{"image": {image}}.Encode()
	var result map[string]any
	if err := client.Post(path, nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("removed "+image, result)
}

func runMSDWrite(cmd *cobra.Command, args []string) error {
	file := args[0]
	data, err := os.ReadFile(file)
	if err != nil {
		return output.WrapCodedError("USAGE", err, fmt.Sprintf("read %s: %v", file, err))
	}

	image := msdWriteImage
	if image == "" {
		image = filepath.Base(file)
	}
	if err := vmRequireYes(msdWriteYes, fmt.Sprintf("upload %q (%s) to MSD storage as %q", file, vmFormatBytes(float64(len(data))), image)); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	path := "/api/msd/write?" + url.Values{
		"prefix":            {msdWritePrefix},
		"image":             {image},
		"remove_incomplete": {vmBoolInt(msdRemoveIncomplete)},
	}.Encode()

	var result map[string]any
	if err := vmRawPost(client, path, "application/octet-stream", data, vmUploadTimeout(cmd), &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("uploaded "+image, result)
}

func runMSDWriteRemote(cmd *cobra.Command, args []string) error {
	remote := args[0]
	if err := vmRequireYes(msdWriteRemoteYes, fmt.Sprintf("download %q into MSD storage", remote)); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	path := "/api/msd/write_remote?" + url.Values{
		"prefix":            {msdWriteRemotePrefix},
		"url":               {remote},
		"remove_incomplete": {vmBoolInt(msdRemoteIncomplete)},
	}.Encode()

	var result map[string]any
	if err := client.Post(path, nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("downloading "+remote, result)
}

func runMSDSetParams(cmd *cobra.Command, args []string) error {
	values := url.Values{}
	for _, arg := range args {
		key, val, ok := strings.Cut(arg, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid parameter %q (expected key=value)", arg))
		}
		values.Set(key, val)
	}
	if cmd.Flags().Changed("cdrom") {
		values.Set("cdrom", strconv.FormatBool(msdSetParamsCDROM))
	}
	if msdSetParamsImage != "" {
		values.Set("image", msdSetParamsImage)
	}
	if len(values) == 0 {
		return output.NewCodedError("USAGE", "at least one parameter is required (key=value, --cdrom, or --image)")
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/msd/set_params?"+values.Encode(), nil, &result); err != nil {
		return vmDeviceError(err)
	}
	// --set may carry secrets (for example password=); mask them from the
	// rendered action and any echoed result.
	return vmRenderAction("set params ("+redact.Params(values.Encode())+")", redact.Map(result))
}

// ---- shared helpers (cmd package) ---------------------------------
//
// These helpers are intentionally prefixed with "vm" so they do not collide
// with helpers introduced by sibling tickets that share this package.

// vmDeviceError converts a transport/device failure into a CodedError with a
// stable code, making the device's error_msg the primary message. Non-API
// errors are returned unchanged.
func vmDeviceError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		msg := strings.TrimSpace(apiErr.Message)
		switch {
		case msg == "":
			msg = apiErr.Error()
		case apiErr.Code != "" && !strings.EqualFold(apiErr.Code, msg):
			msg = apiErr.Code + ": " + msg
		}
		return output.WrapCodedError(vmErrorCode(apiErr.Status), apiErr, msg)
	}
	return err
}

// vmErrorCode maps an HTTP status to a stable machine-readable code.
func vmErrorCode(status int) string {
	switch {
	case status == 401:
		return "AUTH_INVALID"
	case status == 403:
		return "FORBIDDEN"
	case status == 404:
		return "NOT_FOUND"
	case status >= 500:
		return "DEVICE_ERROR"
	default:
		return "ERROR"
	}
}

// vmRegisterConfirm registers the shared destructive-operation confirmation
// flags on cmd: --yes and its GNU-style synonym -f/--force. Both flags write to
// the same target, so supplying either one proceeds; the global --dry-run flag
// always wins (see vmRequireYes).
func vmRegisterConfirm(cmd *cobra.Command, target *bool, yesUsage string) {
	cmd.Flags().BoolVar(target, "yes", false, yesUsage)
	cmd.Flags().BoolVarP(target, "force", "f", false, "Synonym for --yes (skip confirmation)")
}

// vmRequireYes enforces the --yes/--force gate for destructive operations.
//
// - --dry-run (global) always renders the would-be action and returns without
// executing, even when --yes/--force is supplied.
// - When yes is false the operation is refused with a coded error describing
// the would-be action and how to proceed, so nothing is silently destroyed.
func vmRequireYes(yes bool, action string) error {
	// The CLI enforces --dry-run centrally before the command runs, so this
	// branch is only reachable when a run function is invoked directly.
	if flagDryRun {
		return output.NewCodedError("CONFIRMATION_REQUIRED",
			fmt.Sprintf("dry run: would %s; re-run without --dry-run to execute", action))
	}
	if yes {
		return nil
	}
	// Deliberately distinct from the --dry-run preview (stdout, exit 0): a
	// missing confirmation is an error (stderr, exit non-zero).
	return output.NewCodedError("CONFIRMATION_REQUIRED",
		fmt.Sprintf("refusing to %s: re-run with --yes (or -f/--force) to proceed", action))
}

// vmRenderAction renders the outcome of a mutating call. The data object is the
// action plus any device result so JSON consumers get a stable envelope.
func vmRenderAction(action string, result map[string]any) error {
	data := map[string]any{"action": action, "ok": true}
	if len(result) > 0 {
		data["result"] = result
	}
	td := output.TableData{Headers: []string{"ACTION"}, Rows: [][]string{{action}}}
	return output.Render(td, data, GetOutputOptions())
}

// vmBoolInt renders a bool as the "1"/"0" the device expects for query flags.
func vmBoolInt(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// vmUploadTimeout returns the per-request timeout for potentially large
// uploads. The default 30s is too short for multi-gigabyte images, so uploads
// run without a per-request deadline unless the user set --timeout explicitly.
func vmUploadTimeout(cmd *cobra.Command) time.Duration {
	if cmd.Flags().Changed("timeout") {
		return flagTimeout
	}
	return 0
}

// vmEnvelope mirrors pkg/api's private response envelope for the raw requests
// that the standard client cannot express (binary bodies, multipart).
type vmEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

// vmAPIErrorPayload is the shape of result when ok is false.
type vmAPIErrorPayload struct {
	Error    string `json:"error"`
	ErrorMsg string `json:"error_msg"`
}

// vmRawPost performs a POST with a raw (non-JSON) body, such as a binary image
// upload. The standard api.Client only supports JSON and urlencoded bodies, so
// this helper issues the request directly against the client's resolved base
// URL and token.
//
// TLS: verification is attempted first and, when it fails with a certificate
// error (the Comet's certificate is self-signed with a 1970-1979 validity), the
// request is retried insecurely — unless --insecure was given (use insecure
// directly) or KVM_TLS_STRICT/GLKVM_TLS_STRICT is set (never fall back).
func vmRawPost(client *api.Client, path, contentType string, body []byte, timeout time.Duration, out any) error {
	if flagInsecure || vmTLSStrict() {
		return vmRawPostOnce(client, path, contentType, body, flagInsecure, timeout, out)
	}

	err := vmRawPostOnce(client, path, contentType, body, false, timeout, out)
	if err != nil && vmIsCertificateError(err) {
		DebugLog("TLS verification failed (%v); retrying insecurely", err)
		return vmRawPostOnce(client, path, contentType, body, true, timeout, out)
	}
	return err
}

func vmRawPostOnce(client *api.Client, path, contentType string, body []byte, insecure bool, timeout time.Duration, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	fullURL := client.BaseURL() + path
	req, err := http.NewRequest(http.MethodPost, fullURL, reader)
	if err != nil {
		// The URL may carry secrets in its query string; redact before the
		// parse error escapes to the user or a --debug log.
		return redact.Error(fmt.Errorf("create request: %w", err))
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if tok := client.Token(); tok != "" {
		req.Header.Set("token", tok)
	}

	hc := &http.Client{
		Timeout: timeout,
		//nolint:gosec // The device commonly uses a self-signed certificate.
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}},
	}
	resp, err := hc.Do(req)
	if err != nil {
		// net/http embeds the full request URL (including secret query
		// parameters such as old_password/new_password) in a *url.Error. Mask
		// it so ANY transport failure is safe, even without --debug.
		return redact.Error(err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	return vmDecodeEnvelope(data, resp.StatusCode, out)
}

// vmDecodeEnvelope interprets the standard {"ok","result"} wrapper for raw
// requests.
func vmDecodeEnvelope(data []byte, status int, out any) error {
	var env vmEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		if status < 200 || status >= 300 {
			return &api.APIError{Code: http.StatusText(status), Message: strings.TrimSpace(string(data)), Status: status}
		}
		return fmt.Errorf("parse response: %w (body: %s)", err, vmTruncate(string(data), 200))
	}

	if !env.OK {
		e := &api.APIError{Status: status}
		var payload vmAPIErrorPayload
		if err := json.Unmarshal(env.Result, &payload); err == nil {
			e.Code = payload.Error
			e.Message = payload.ErrorMsg
		}
		if e.Code == "" {
			e.Code = http.StatusText(status)
		}
		if e.Message == "" {
			e.Message = e.Code
		}
		return e
	}
	if status < 200 || status >= 300 {
		return &api.APIError{Code: http.StatusText(status), Message: vmTruncate(strings.TrimSpace(string(data)), 500), Status: status}
	}
	if out == nil {
		return nil
	}

	result := bytes.TrimSpace(env.Result)
	if len(result) == 0 || bytes.Equal(result, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(result, out); err != nil {
		return fmt.Errorf("parse result: %w", err)
	}
	return nil
}

// vmIsCertificateError reports whether err is a TLS certificate-verification
// failure.
func vmIsCertificateError(err error) bool {
	if err == nil {
		return false
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "x509") || strings.Contains(msg, "certificate")
}

// vmTLSStrict reports whether the user forbade the insecure TLS fallback.
func vmTLSStrict() bool {
	for _, k := range []string{"KVM_TLS_STRICT", "GLKVM_TLS_STRICT"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// vmTruncate shortens s to max characters, appending an ellipsis.
func vmTruncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// vmAsMap coerces v to map[string]any, returning false for any other type.
func vmAsMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case api.RawMap:
		return map[string]any(t), true
	default:
		return nil, false
	}
}

// vmSortedKeys returns the map keys in ascending order.
func vmSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// vmFlattenRows flattens a (possibly nested) JSON object into sorted
// dotted-key/value rows. Nested objects recurse; arrays and scalars are encoded
// with vmValueString.
func vmFlattenRows(prefix string, m map[string]any) [][]string {
	keys := vmSortedKeys(m)

	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := vmAsMap(m[k]); ok {
			rows = append(rows, vmFlattenRows(key, nested)...)
			continue
		}
		rows = append(rows, []string{key, vmValueString(m[k])})
	}
	return rows
}

// vmValueString renders a JSON value for table/plaintext output. Nested objects
// and arrays are emitted as compact JSON.
func vmValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// vmFloat extracts a numeric value from the JSON-decoded representation.
func vmFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	case int64:
		return float64(t)
	case int:
		return float64(t)
	default:
		return 0
	}
}

// vmFormatBytes renders a byte count as a human-readable string.
func vmFormatBytes(n float64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%.0f B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	n /= unit
	for n >= unit && i < len(units)-1 {
		n /= unit
		i++
	}
	return fmt.Sprintf("%.2f %s", n, units[i])
}
