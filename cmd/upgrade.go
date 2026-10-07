package cmd

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// upgradeCmd groups firmware upgrade operations: inspecting the installed and
// available versions, downloading a firmware image, uploading a local image,
// exporting the device log, and reading/writing the EDID.
//
// Operations that can interrupt the device (reboot, factory reset, starting an
// install, uploading an image) are destructive and require --yes.
var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Inspect and manage firmware upgrades",
	Long: `Inspect and manage the remote KVM's firmware.

Read-only commands (version, check, status, log, edid) inspect the current
firmware, the latest available release, download progress, the device log, and
the stored EDID. The remaining commands change device state: 'start' installs a
previously downloaded image, 'upload' installs a local image, 'download'
fetches the latest image, 'cancel' aborts a download, 'reboot' restarts the
device, and 'reset' restores factory defaults.

Destructive commands (start, upload, reboot, reset) require --yes. The download
and upload endpoints honour --timeout; when --timeout is not set they run
without a per-request deadline because firmware images are large.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli upgrade version
  kvm-cli upgrade check --json
  kvm-cli upgrade status
  kvm-cli upgrade log --output device-log.zip
  kvm-cli upgrade edid
  kvm-cli upgrade start --save-config --yes
  kvm-cli upgrade reboot --yes`,
}

var (
	k10UpgradeStartYes   bool
	k10UpgradeUploadYes  bool
	k10UpgradeRebootYes  bool
	k10UpgradeResetYes   bool
	k10UpgradeSaveConfig bool
	k10UpgradeLogOutput  string
)

var upgradeVersionCmd = &cobra.Command{
	Use:     "version",
	Short:   "Show the installed firmware version (GET /api/upgrade/version)",
	Long:    "Show the device model and the currently installed firmware version.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade version\n  kvm-cli upgrade version --json",
	RunE:    runUpgradeVersion,
}

var upgradeCheckCmd = &cobra.Command{
	Use:     "check",
	Aliases: []string{"compare"},
	Short:   "Compare installed and available firmware (GET /api/upgrade/compare)",
	Long: `Compare the installed firmware against the latest release.

The device returns the local and server versions, the server release notes, and
any error encountered while contacting the update server. Use --json to read
the complete document; --fields and --jq project it.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade check\n  kvm-cli upgrade check --json\n  kvm-cli upgrade check --json --jq .server_version",
	RunE:    runUpgradeCheck,
}

var upgradeStatusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"download-info"},
	Short:   "Show firmware download progress (GET /api/upgrade/download_info)",
	Long:    "Show the number of bytes downloaded so far and the total image size for an in-progress firmware download.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade status\n  kvm-cli upgrade status --json",
	RunE:    runUpgradeStatus,
}

var upgradeLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Download the device log archive (GET /api/upgrade/log)",
	Long: `Download the device log bundle.

The endpoint returns a ZIP archive rather than JSON, so the bytes are written to
a file (default: kvm-upgrade-log.zip, override with --output) and a summary is
printed. Use --output - to stream the archive to stdout.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade log\n  kvm-cli upgrade log --output device-log.zip\n  kvm-cli upgrade log --output - > log.zip",
	RunE:    runUpgradeLog,
}

var upgradeEdidCmd = &cobra.Command{
	Use:     "edid",
	Aliases: []string{"get-edid"},
	Short:   "Show the stored EDID block (GET /api/upgrade/get_edid)",
	Long:    "Show the EDID block the device presents to the controlled computer, as a hex-encoded string.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade edid\n  kvm-cli upgrade get-edid --json",
	RunE:    runUpgradeEdid,
}

var upgradeStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Install the downloaded firmware (POST /api/upgrade/start) — DESTRUCTIVE",
	Long: `Start installing the firmware image previously fetched with 'download'.

Destructive: the device reboots and the install cannot be interrupted, so --yes
is required. Use --save-config=false to discard configuration during the
upgrade (configuration is preserved by default).`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade start --yes\n  kvm-cli upgrade start --save-config=false --yes",
	RunE:    runUpgradeStart,
}

var upgradeUploadCmd = &cobra.Command{
	Use:   "upload FILE",
	Short: "Upload and install a local firmware image (POST /api/upgrade/upload) — DESTRUCTIVE",
	Long: `Upload a local firmware image to the device and start the install.

Destructive: the device reboots and the install cannot be interrupted, so --yes
is required. The image is sent as multipart/form-data and may be large; the
request runs without a per-request deadline unless --timeout is given.`,
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli upgrade upload ./RM1PE-V1.10.1.img --yes",
	RunE:    runUpgradeUpload,
}

var upgradeDownloadCmd = &cobra.Command{
	Use:   "download",
	Short: "Download the latest firmware image (POST /api/upgrade/download)",
	Long: `Ask the device to download the latest firmware image from the update server.

The download runs in the background; poll progress with 'upgrade status' and
abort it with 'upgrade cancel'. The request runs without a per-request deadline
unless --timeout is given, because the image is large.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade download\n  kvm-cli upgrade download --timeout 10m",
	RunE:    runUpgradeDownload,
}

var upgradeCancelCmd = &cobra.Command{
	Use:     "cancel",
	Aliases: []string{"download-cancel"},
	Short:   "Cancel an in-progress firmware download (POST /api/upgrade/download_cancel)",
	Long:    "Abort the firmware download currently in progress and discard the partial image.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade cancel\n  kvm-cli upgrade download-cancel",
	RunE:    runUpgradeCancel,
}

var upgradeRebootCmd = &cobra.Command{
	Use:     "reboot",
	Short:   "Reboot the device (POST /api/upgrade/reboot) — DESTRUCTIVE",
	Long:    "Reboot the remote KVM. Destructive: the device becomes unreachable for a short time, so --yes is required.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade reboot --yes\n  kvm-cli upgrade reboot          # dry run",
	RunE:    runUpgradeReboot,
}

var upgradeResetCmd = &cobra.Command{
	Use:     "reset",
	Short:   "Restore factory defaults (POST /api/upgrade/reset_default) — DESTRUCTIVE",
	Long:    "Restore the device to factory defaults, wiping all configuration. Destructive: requires --yes.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli upgrade reset --yes\n  kvm-cli upgrade reset          # dry run",
	RunE:    runUpgradeReset,
}

func init() {
	vmRegisterConfirm(upgradeStartCmd, &k10UpgradeStartYes, "Confirm installing the downloaded firmware (required to proceed)")
	upgradeStartCmd.Flags().BoolVar(&k10UpgradeSaveConfig, "save-config", true, "Preserve configuration across the upgrade")

	vmRegisterConfirm(upgradeUploadCmd, &k10UpgradeUploadYes, "Confirm uploading and installing the image (required to proceed)")

	vmRegisterConfirm(upgradeRebootCmd, &k10UpgradeRebootYes, "Confirm rebooting the device (required to proceed)")
	vmRegisterConfirm(upgradeResetCmd, &k10UpgradeResetYes, "Confirm restoring factory defaults (required to proceed)")

	upgradeLogCmd.Flags().StringVarP(&k10UpgradeLogOutput, "output", "o", "kvm-upgrade-log.zip", "Write the log archive to this file (- for stdout)")

	for _, c := range []*cobra.Command{
		upgradeStartCmd,
		upgradeUploadCmd,
		upgradeDownloadCmd,
		upgradeCancelCmd,
		upgradeRebootCmd,
		upgradeResetCmd,
	} {
		MarkWrite(c)
	}

	upgradeCmd.AddCommand(
		upgradeVersionCmd,
		upgradeCheckCmd,
		upgradeStatusCmd,
		upgradeLogCmd,
		upgradeEdidCmd,
		upgradeStartCmd,
		upgradeUploadCmd,
		upgradeDownloadCmd,
		upgradeCancelCmd,
		upgradeRebootCmd,
		upgradeResetCmd,
	)
	rootCmd.AddCommand(upgradeCmd)
}

func runUpgradeVersion(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var v api.UpgradeVersion
	if err := client.Get("/api/upgrade/version", &v); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"MODEL", "VERSION"},
		Rows:    [][]string{{v.Model, v.Version}},
	}
	return output.Render(td, v, GetOutputOptions())
}

func runUpgradeCheck(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/upgrade/compare", &result); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runUpgradeStatus(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/upgrade/download_info", &result); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runUpgradeLog(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	data, contentType, err := client.GetRaw("/api/upgrade/log")
	if err != nil {
		return vmDeviceError(err)
	}

	if k10UpgradeLogOutput == "-" {
		if _, werr := os.Stdout.Write(data); werr != nil {
			return output.WrapCodedError("ERROR", werr, fmt.Sprintf("write log archive to stdout: %v", werr))
		}
		return nil
	}

	if err := os.WriteFile(k10UpgradeLogOutput, data, 0o644); err != nil {
		return output.WrapCodedError("ERROR", err, fmt.Sprintf("write %s: %v", k10UpgradeLogOutput, err))
	}

	summary := map[string]any{
		"output":       k10UpgradeLogOutput,
		"bytes":        len(data),
		"content_type": contentType,
	}
	td := output.TableData{
		Headers: []string{"OUTPUT", "BYTES", "CONTENT-TYPE"},
		Rows:    [][]string{{k10UpgradeLogOutput, vmValueString(len(data)), contentType}},
	}
	return output.Render(td, summary, GetOutputOptions())
}

func runUpgradeEdid(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/upgrade/get_edid", &result); err != nil {
		return vmDeviceError(err)
	}

	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    k10FlattenRows("", result),
	}
	return output.Render(td, result, GetOutputOptions())
}

func runUpgradeStart(cmd *cobra.Command, args []string) error {
	action := "install the downloaded firmware"
	if !k10UpgradeSaveConfig {
		action += " without preserving configuration"
	}
	if err := vmRequireYes(k10UpgradeStartYes, action); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	values := url.Values{"save_config": {vmBoolInt(k10UpgradeSaveConfig)}}
	var result map[string]any
	if err := vmRawPost(client, "/api/upgrade/start?"+values.Encode(), "", nil, vmUploadTimeout(cmd), &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("started firmware install (save_config="+vmBoolInt(k10UpgradeSaveConfig)+")", result)
}

func runUpgradeUpload(cmd *cobra.Command, args []string) error {
	file := args[0]
	if err := vmRequireYes(k10UpgradeUploadYes, fmt.Sprintf("upload and install firmware image %q", file)); err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return output.WrapCodedError("USAGE", err, fmt.Sprintf("read %s: %v", file, err))
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
	if err := vmRawPost(client, "/api/upgrade/upload", contentType, body, vmUploadTimeout(cmd), &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("uploaded firmware image "+filepath.Base(file), result)
}

func runUpgradeDownload(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := vmRawPost(client, "/api/upgrade/download", "", nil, vmUploadTimeout(cmd), &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("started firmware download", result)
}

func runUpgradeCancel(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/upgrade/download_cancel", nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("canceled firmware download", result)
}

func runUpgradeReboot(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(k10UpgradeRebootYes, "reboot the device"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/upgrade/reboot", nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("rebooting the device", result)
}

func runUpgradeReset(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(k10UpgradeResetYes, "restore factory defaults (this wipes all configuration)"); err != nil {
		return err
	}

	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post("/api/upgrade/reset_default", nil, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction("restored factory defaults", result)
}
