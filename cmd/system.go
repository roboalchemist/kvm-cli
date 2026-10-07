package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// systemCmd groups the read-only system introspection endpoints of the GLKVM
// REST API. Every subcommand is a plain GET and never mutates device state; the
// matching write endpoints are deliberately out of scope here.
var systemCmd = &cobra.Command{
	Use:   "system",
	Short: "Inspect system information (capability, config, network, time, otg)",
	Long: `Inspect the remote KVM's system information.

All subcommands are read-only: they issue GET requests and never modify device
state. Output honours the global --json/--plaintext/--format/--fields/--jq
flags; nested objects are flattened into key/value tables for humans.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli system capability
  kvm-cli system config --json
  kvm-cli system network
  kvm-cli system timezone --list`,
}

var systemCapabilityCmd = &cobra.Command{
	Use:     "capability",
	Short:   "Show hardware capabilities (GET /api/system/capability)",
	Long:    "Show the device's hardware capability map: CPU model, MIPI bridge, OTG/USB versions, and partition paths.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system capability\n  kvm-cli system capability --json",
	RunE:    runSystemCapability,
}

var systemParamCmd = &cobra.Command{
	Use:     "param",
	Short:   "Show system parameters (GET /api/system/get_param)",
	Long:    "Show the device's system parameters as a flat key/value map (USB identity, camera/mic names, privacy flags).",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system param\n  kvm-cli system param --json --fields otg_product",
	RunE:    runSystemParam,
}

var systemConfigCmd = &cobra.Command{
	Use:     "config",
	Short:   "Show system configuration (GET /api/system/get_config)",
	Long:    "Show the device's nested configuration object (keyboard/mouse, video, HID, tips, shortcut definitions).",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system config\n  kvm-cli system config --json\n  kvm-cli system config --jq .keymap",
	RunE:    runSystemConfig,
}

var systemHostnameCmd = &cobra.Command{
	Use:     "hostname",
	Short:   "Show the device hostname (GET /api/system/get_hostname)",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system hostname\n  kvm-cli system hostname --plaintext",
	RunE:    runSystemHostname,
}

var systemNetworkCmd = &cobra.Command{
	Use:     "network",
	Short:   "Show network configuration (GET /api/system/get_network_config)",
	Long:    "Show the active network interface configuration: address, netmask, gateway, MAC, DHCP mode, and DNS servers.",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system network\n  kvm-cli system network --json",
	RunE:    runSystemNetwork,
}

var systemTimeCmd = &cobra.Command{
	Use:     "time",
	Short:   "Show device time and timezone (GET /api/system/time)",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system time\n  kvm-cli system time --json",
	RunE:    runSystemTime,
}

// systemTimezoneList selects the full timezone catalogue over the current zone.
var systemTimezoneList bool

var systemTimezoneCmd = &cobra.Command{
	Use:   "timezone",
	Short: "Show the current timezone, or list available ones",
	Long: `Show the device's current timezone, or with --list enumerate every timezone
the device understands.

The current zone is read from /api/system/time (the /api/system/timezone
endpoint is write-only on this firmware and is reserved for setting the zone).
With --list, /api/system/timezone/list is queried.`,
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system timezone\n  kvm-cli system timezone --list\n  kvm-cli system timezone --list --json",
	RunE:    runSystemTimezone,
}

var systemOTGCmd = &cobra.Command{
	Use:     "otg",
	Short:   "Show USB OTG function state (GET /api/system/otg_functions)",
	Long:    "Show the USB On-The-Go gadget functions the device presents to the target (keyboard, mouse, camera, mic, MTP, CD-ROM, flash).",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system otg\n  kvm-cli system otg --json",
	RunE:    runSystemOTG,
}

var systemFirewallCmd = &cobra.Command{
	Use:     "firewall",
	Short:   "Show firewall configuration (GET /api/system/get_firewall_config)",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli system firewall\n  kvm-cli system firewall --json",
	RunE:    runSystemFirewall,
}

func init() {
	systemTimezoneCmd.Flags().BoolVar(&systemTimezoneList, "list", false, "List every available timezone instead of the current one")

	systemCmd.AddCommand(
		systemCapabilityCmd,
		systemParamCmd,
		systemConfigCmd,
		systemHostnameCmd,
		systemNetworkCmd,
		systemTimeCmd,
		systemTimezoneCmd,
		systemOTGCmd,
		systemFirewallCmd,
	)
	rootCmd.AddCommand(systemCmd)
}

func runSystemCapability(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var cap api.Capability
	if err := client.Get("/api/system/capability", &cap); err != nil {
		return cliDeviceError(err)
	}

	data := any(cap.Capability)
	if cap.Capability == nil {
		data = cap.Raw
	}
	m, _ := cliAsMap(data)
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    cliFlattenRows("", m),
	}
	return output.Render(td, data, GetOutputOptions())
}

func runSystemParam(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/system/get_param", &result); err != nil {
		return cliDeviceError(err)
	}

	params := cliWithout(result, "success")
	td := output.TableData{
		Headers: []string{"PARAMETER", "VALUE"},
		Rows:    cliFlattenRows("", params),
	}
	return output.Render(td, params, GetOutputOptions())
}

func runSystemConfig(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/system/get_config", &result); err != nil {
		return cliDeviceError(err)
	}

	// The payload is {"config": {...}, "success": true}; prefer the nested
	// config object but fall back to the whole result if the shape changes.
	cfg, ok := cliAsMap(result["config"])
	if !ok {
		cfg = cliWithout(result, "success")
	}

	td := output.TableData{
		Headers: []string{"SETTING", "VALUE"},
		Rows:    cliFlattenRows("", cfg),
	}
	return output.Render(td, cfg, GetOutputOptions())
}

func runSystemHostname(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var h api.Hostname
	if err := client.Get("/api/system/get_hostname", &h); err != nil {
		return cliDeviceError(err)
	}

	data := map[string]any{"hostname": h.Hostname}
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    [][]string{{"hostname", h.Hostname}},
	}
	return output.Render(td, data, GetOutputOptions())
}

func runSystemNetwork(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var n api.NetworkConfig
	if err := client.Get("/api/system/get_network_config", &n); err != nil {
		return cliDeviceError(err)
	}

	data := networkData(n)
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    cliFlattenRows("", data),
	}
	return output.Render(td, data, GetOutputOptions())
}

func runSystemTime(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/system/time", &result); err != nil {
		return cliDeviceError(err)
	}

	data := cliWithout(result, "success")
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    timeRows(result),
	}
	return output.Render(td, data, GetOutputOptions())
}

func runSystemTimezone(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	if systemTimezoneList {
		var result map[string]any
		if err := client.Get("/api/system/timezone/list", &result); err != nil {
			return cliDeviceError(err)
		}
		data := cliWithout(result, "success")

		td := output.TableData{Headers: []string{"TIMEZONE"}}
		for _, tz := range cliStringSlice(result["timezones"]) {
			td.Rows = append(td.Rows, []string{tz})
		}
		if count, ok := data["count"]; ok {
			td.Footer = fmt.Sprintf("%s timezones", cliValueString(count))
		}
		return output.Render(td, data, GetOutputOptions())
	}

	// Current zone. /api/system/timezone is write-only on this firmware (POST
	// to set), so the readable current value comes from /api/system/time.
	var t map[string]any
	if err := client.Get("/api/system/time", &t); err != nil {
		return cliDeviceError(err)
	}
	name := cliValueString(t["timezone_name"])
	data := map[string]any{"timezone_name": name}
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    [][]string{{"timezone_name", name}},
	}
	return output.Render(td, data, GetOutputOptions())
}

func runSystemOTG(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var o api.OTGFunctions
	if err := client.Get("/api/system/otg_functions", &o); err != nil {
		return cliDeviceError(err)
	}

	data := any(o.Raw)
	if o.Raw == nil {
		data = o
	}
	m, _ := cliAsMap(data)
	td := output.TableData{
		Headers: []string{"FUNCTION", "STATE"},
		Rows:    cliFlattenRows("", m),
	}
	return output.Render(td, data, GetOutputOptions())
}

func runSystemFirewall(cmd *cobra.Command, args []string) error {
	client, err := NewClient(cmd)
	if err != nil {
		return cliDeviceError(err)
	}

	var result map[string]any
	if err := client.Get("/api/system/get_firewall_config", &result); err != nil {
		return cliDeviceError(err)
	}

	data := cliWithout(result, "success")
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    cliFlattenRows("", data),
	}
	return output.Render(td, data, GetOutputOptions())
}

// networkData returns the network configuration as a plain JSON-shaped map so
// that every output mode (including YAML) sees consistent keys and no leaked
// helper fields. It prefers the raw decoded config for forward compatibility
// and falls back to the modelled fields.
func networkData(n api.NetworkConfig) map[string]any {
	if cfg, ok := cliAsMap(n.Raw["config"]); ok {
		return cfg
	}
	m := map[string]any{}
	if n.Config.Interface != "" {
		m["interface"] = n.Config.Interface
	}
	if n.Config.State != "" {
		m["state"] = n.Config.State
	}
	if n.Config.IPAddress != "" {
		m["ip_address"] = n.Config.IPAddress
	}
	if n.Config.Netmask != "" {
		m["netmask"] = n.Config.Netmask
	}
	if n.Config.Gateway != "" {
		m["gateway"] = n.Config.Gateway
	}
	if n.Config.MACAddress != "" {
		m["mac_address"] = n.Config.MACAddress
	}
	if n.Config.IsDHCP != nil {
		m["is_dhcp"] = *n.Config.IsDHCP
	}
	if len(n.Config.DNSServers) > 0 {
		m["dns_servers"] = n.Config.DNSServers
	}
	return m
}

// timeRows renders /api/system/time as key/value rows, formatting the Unix
// timestamp in the device's own timezone when it can be loaded.
func timeRows(result map[string]any) [][]string {
	rows := [][]string{}
	if raw, ok := result["time"]; ok {
		rows = append(rows, []string{"time", formatUnix(raw, cliValueString(result["timezone_name"]))})
		rows = append(rows, []string{"unix", cliValueString(raw)})
	}
	if v, ok := result["time_zone"]; ok {
		rows = append(rows, []string{"time_zone", cliValueString(v)})
	}
	if v, ok := result["timezone_name"]; ok {
		rows = append(rows, []string{"timezone_name", cliValueString(v)})
	}
	return rows
}

// formatUnix renders a JSON number of Unix seconds as RFC3339 in locName, or in
// UTC when the zone cannot be resolved.
func formatUnix(raw any, locName string) string {
	f, ok := cliFloat(raw)
	if !ok {
		return cliValueString(raw)
	}
	t := time.Unix(int64(f), 0).UTC()
	if locName != "" {
		if loc, err := time.LoadLocation(locName); err == nil {
			t = t.In(loc)
		}
	}
	return t.Format(time.RFC3339)
}

// ---- shared helpers -----------------------------------------------------

// cliDeviceError converts a transport/device failure into a CodedError with a
// stable code, making the device's error_msg the primary message. Non-API
// errors are returned unchanged.
func cliDeviceError(err error) error {
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
		// Non-envelope failures (for example an nginx 502 page) can carry
		// multi-line bodies; collapse them to a single readable line.
		msg = strings.Join(strings.Fields(msg), " ")
		return output.WrapCodedError(cliErrorCode(apiErr.Status), apiErr, msg)
	}
	return err
}

// cliErrorCode maps an HTTP status to a stable machine-readable code.
func cliErrorCode(status int) string {
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

// cliAsMap coerces v to map[string]any, returning false for any other type.
func cliAsMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case api.RawMap:
		return map[string]any(t), true
	default:
		return nil, false
	}
}

// cliWithout returns a shallow copy of m with the named keys removed.
func cliWithout(m map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

// cliFlattenRows flattens a (possibly nested) JSON object into sorted
// dotted-key/value rows. Nested objects recurse; arrays and scalars are encoded
// with cliValueString.
func cliFlattenRows(prefix string, m map[string]any) [][]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := cliAsMap(m[k]); ok {
			rows = append(rows, cliFlattenRows(key, nested)...)
			continue
		}
		rows = append(rows, []string{key, cliValueString(m[k])})
	}
	return rows
}

// cliGetPath resolves a dotted path within nested maps.
func cliGetPath(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, p := range path {
		mm, ok := cliAsMap(cur)
		if !ok {
			return nil, false
		}
		cur, ok = mm[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// cliValueString renders a JSON value for table/plaintext output. Nested
// objects and arrays are emitted as compact JSON.
func cliValueString(v any) string {
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

// cliFloat extracts a numeric value from the JSON-decoded representation.
func cliFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	default:
		return 0, false
	}
}

// cliStringSlice coerces v to a []string, accepting the JSON []any shape.
func cliStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, cliValueString(item))
		}
		return out
	default:
		return nil
	}
}
