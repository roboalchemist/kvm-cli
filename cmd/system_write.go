package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// This file adds the write half of the system API to the existing read-only
// systemCmd (defined in cmd/system.go). Every subcommand here mutates device
// state and therefore requires --yes; the matching read endpoints live in
// system.go and are untouched.

// syswYes is shared by every system write subcommand. Only one subcommand runs
// per invocation, so a single backing variable is safe and keeps the flag
// definition consistent.
var syswYes bool

var (
	syswSetConfig    []string
	syswSetNetwork   []string
	syswSetFirewall  []string
	syswSetParam     []string
	syswSetTime      []string
	syswSetTimezone  []string
	syswConfigFile   string
	syswFirewallFile string
	syswNetMode      string
	syswNetIP        string
	syswNetNetmask   string
	syswNetGateway   string
	syswNetDNS       string
	syswFWEnable     bool
	syswFWEnableV6   bool
	syswCertFile     string
	syswKeyFile      string
	syswCertDefault  bool
)

var systemSetConfigCmd = &cobra.Command{
	Use:   "set-config",
	Short: "Write system configuration (POST /api/system/set_config) — DESTRUCTIVE",
	Long: `Write one or more keys into the device's stored configuration object.

The SPA posts the full configuration object; this command builds that object
from --set key=value pairs (and/or a JSON object supplied with --file or stdin)
so only the keys you name are included. Requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli system set-config --set theme_mode=dark --yes
  kvm-cli system set-config --file config.json --yes
  cat config.json | kvm-cli system set-config --file - --yes
  kvm-cli system set-config --set theme_mode=dark          # dry run`,
	RunE: runSystemSetConfig,
}

var systemSetNetworkCmd = &cobra.Command{
	Use:   "set-network",
	Short: "Write network configuration (POST /api/system/set_network_config) — DESTRUCTIVE",
	Long: `Apply network settings to the device. Parameters are sent as query
parameters, matching the SPA.

Use --mode dhcp for automatic addressing, or --mode static together with --ip,
--netmask and --gateway. --dns accepts a comma-separated list. Arbitrary
key=value pairs may be supplied with --set. Requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli system set-network --mode dhcp --yes
  kvm-cli system set-network --mode static --ip 10.0.0.5 --netmask 255.255.255.0 --gateway 10.0.0.1 --dns 1.1.1.1,8.8.8.8 --yes
  kvm-cli system set-network --set ip_address=10.0.0.5 --yes
  kvm-cli system set-network --mode dhcp                    # dry run`,
	RunE: runSystemSetNetwork,
}

var systemSetHostnameCmd = &cobra.Command{
	Use:   "set-hostname HOSTNAME",
	Short: "Write the device hostname (POST /api/system/set_hostname) — DESTRUCTIVE",
	Long:  "Change the device's hostname. Requires --yes.",
	Args:  cobra.ExactArgs(1),
	Example: `  kvm-cli system set-hostname glkvm --yes
  kvm-cli system set-hostname glkvm          # dry run`,
	RunE: runSystemSetHostname,
}

var systemSetFirewallCmd = &cobra.Command{
	Use:   "set-firewall",
	Short: "Write firewall configuration (POST /api/system/set_firewall_config) — DESTRUCTIVE",
	Long: `Write the cellular firewall configuration as a JSON body.

The device expects an object such as {"enable": true, "enable_v6": true,
"whitelist": {...}}. Build a partial object with --enable/--enable-v6/--set, or
supply a complete object with --file (or stdin via --file -). Requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli system set-firewall --enable --enable-v6 --yes
  kvm-cli system set-firewall --file firewall.json --yes
  kvm-cli system set-firewall --enable                     # dry run`,
	RunE: runSystemSetFirewall,
}

var systemSetParamCmd = &cobra.Command{
	Use:   "set-param",
	Short: "Write system parameters (POST /api/system/set_param) — DESTRUCTIVE",
	Long: `Set device parameters (USB identity, camera/mic names, privacy flags,
MSD partition, ...). Parameters are sent as query parameters. Requires --yes.

Read the current values with 'kvm-cli system param'.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli system set-param --set privacy_enable=true --yes
  kvm-cli system set-param --set otg_product_id=0x6973 --set otg_vendor_id=0x6940 --yes
  kvm-cli system set-param --set privacy_enable=true          # dry run`,
	RunE: runSystemSetParam,
}

var systemSSLCertCmd = &cobra.Command{
	Use:   "ssl-cert",
	Short: "Read or write the TLS certificate (GET/POST /api/system/ssl_cert)",
	Long: `Read the device's TLS certificate, or install a replacement.

With no flags the current certificate is shown (GET). Supplying --cert-file and
--key-file installs a replacement (POST); --default resets to the built-in
self-signed certificate. Installation requires --yes.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli system ssl-cert
  kvm-cli system ssl-cert --json
  kvm-cli system ssl-cert --cert-file cert.pem --key-file key.pem --yes
  kvm-cli system ssl-cert --default --yes`,
	RunE: runSystemSSLCert,
}

var systemSetTimeCmd = &cobra.Command{
	Use:   "set-time [UNIX-SECONDS]",
	Short: "Set the device clock (POST /api/system/time) — DESTRUCTIVE",
	Long: `Set the device time. Pass a Unix timestamp positionally, or supply
arbitrary query parameters with --set. Requires --yes.`,
	Args: cobra.MaximumNArgs(1),
	Example: `  kvm-cli system set-time 1760000000 --yes
  kvm-cli system set-time --set time=1760000000 --yes
  kvm-cli system set-time 1760000000                     # dry run`,
	RunE: runSystemSetTime,
}

var systemSetTimezoneCmd = &cobra.Command{
	Use:   "set-timezone TIMEZONE",
	Short: "Set the device timezone (POST /api/system/timezone) — DESTRUCTIVE",
	Long: `Set the device timezone. List valid names with
'kvm-cli system timezone --list'. Requires --yes.`,
	Args: cobra.ExactArgs(1),
	Example: `  kvm-cli system set-timezone America/Los_Angeles --yes
  kvm-cli system set-timezone America/Los_Angeles          # dry run`,
	RunE: runSystemSetTimezone,
}

func init() {
	systemSetConfigCmd.Flags().StringArrayVar(&syswSetConfig, "set", nil, "key=value to include (repeatable)")
	systemSetConfigCmd.Flags().StringVar(&syswConfigFile, "file", "", "JSON object to send (path or - for stdin)")

	systemSetNetworkCmd.Flags().StringVar(&syswNetMode, "mode", "", "Addressing mode: dhcp or static")
	systemSetNetworkCmd.Flags().StringVar(&syswNetIP, "ip", "", "Static IP address")
	systemSetNetworkCmd.Flags().StringVar(&syswNetNetmask, "netmask", "", "Static netmask")
	systemSetNetworkCmd.Flags().StringVar(&syswNetGateway, "gateway", "", "Static gateway")
	systemSetNetworkCmd.Flags().StringVar(&syswNetDNS, "dns", "", "Comma-separated DNS servers")
	systemSetNetworkCmd.Flags().StringArrayVar(&syswSetNetwork, "set", nil, "key=value to include (repeatable)")

	systemSetFirewallCmd.Flags().BoolVar(&syswFWEnable, "enable", false, "Enable the firewall")
	systemSetFirewallCmd.Flags().BoolVar(&syswFWEnableV6, "enable-v6", false, "Enable the IPv6 firewall")
	systemSetFirewallCmd.Flags().StringArrayVar(&syswSetFirewall, "set", nil, "key=value to include (repeatable)")
	systemSetFirewallCmd.Flags().StringVar(&syswFirewallFile, "file", "", "JSON object to send (path or - for stdin)")

	systemSetParamCmd.Flags().StringArrayVar(&syswSetParam, "set", nil, "key=value parameter (repeatable)")

	systemSSLCertCmd.Flags().StringVar(&syswCertFile, "cert-file", "", "PEM certificate file to install")
	systemSSLCertCmd.Flags().StringVar(&syswKeyFile, "key-file", "", "PEM private-key file to install")
	systemSSLCertCmd.Flags().BoolVar(&syswCertDefault, "default", false, "Reset to the built-in self-signed certificate")

	systemSetTimeCmd.Flags().StringArrayVar(&syswSetTime, "set", nil, "key=value to include (repeatable)")

	systemSetTimezoneCmd.Flags().StringArrayVar(&syswSetTimezone, "set", nil, "extra key=value to include (repeatable)")

	// Attach every write subcommand to the existing read-only systemCmd.
	for _, c := range []*cobra.Command{
		systemSetConfigCmd,
		systemSetNetworkCmd,
		systemSetHostnameCmd,
		systemSetFirewallCmd,
		systemSetParamCmd,
		systemSSLCertCmd,
		systemSetTimeCmd,
		systemSetTimezoneCmd,
	} {
		vmRegisterConfirm(c, &syswYes, "Confirm this destructive write (required to proceed)")
		MarkWrite(c)
	}

	systemCmd.AddCommand(
		systemSetConfigCmd,
		systemSetNetworkCmd,
		systemSetHostnameCmd,
		systemSetFirewallCmd,
		systemSetParamCmd,
		systemSSLCertCmd,
		systemSetTimeCmd,
		systemSetTimezoneCmd,
	)
}

func runSystemSetConfig(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(syswYes, "write system configuration"); err != nil {
		return err
	}

	body, err := syswJSONBody(syswConfigFile, syswSetConfig)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return output.NewCodedError("USAGE", "nothing to set: provide --set key=value or --file")
	}

	return syswPostAction(cmd, "set system configuration", "/api/system/set_config", body)
}

func runSystemSetNetwork(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(syswYes, "write network configuration"); err != nil {
		return err
	}

	values := url.Values{}
	syswSetIfChanged(cmd, values, "mode", &syswNetMode)
	syswSetIfChanged(cmd, values, "ip_address", &syswNetIP)
	syswSetIfChanged(cmd, values, "netmask", &syswNetNetmask)
	syswSetIfChanged(cmd, values, "gateway", &syswNetGateway)
	syswSetIfChanged(cmd, values, "dns_servers", &syswNetDNS)
	if err := syswMergeSets(values, syswSetNetwork); err != nil {
		return err
	}
	if len(values) == 0 {
		return output.NewCodedError("USAGE", "nothing to set: provide --mode/--ip/... or --set")
	}

	return syswPostValues(cmd, "set network configuration", "/api/system/set_network_config", values)
}

func runSystemSetHostname(cmd *cobra.Command, args []string) error {
	hostname := strings.TrimSpace(args[0])
	if err := vmRequireYes(syswYes, fmt.Sprintf("set hostname to %q", hostname)); err != nil {
		return err
	}
	if hostname == "" {
		return output.NewCodedError("USAGE", "hostname must not be empty")
	}

	values := url.Values{"hostname": {hostname}}
	return syswPostValues(cmd, "set hostname to "+hostname, "/api/system/set_hostname", values)
}

func runSystemSetFirewall(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(syswYes, "write firewall configuration"); err != nil {
		return err
	}

	body, err := syswJSONBody(syswFirewallFile, syswSetFirewall)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("enable") {
		body["enable"] = syswFWEnable
	}
	if cmd.Flags().Changed("enable-v6") {
		body["enable_v6"] = syswFWEnableV6
	}
	if len(body) == 0 {
		return output.NewCodedError("USAGE", "nothing to set: provide --enable/--enable-v6, --set, or --file")
	}

	return syswPostAction(cmd, "set firewall configuration", "/api/system/set_firewall_config", body)
}

func runSystemSetParam(cmd *cobra.Command, args []string) error {
	if err := vmRequireYes(syswYes, "write system parameters"); err != nil {
		return err
	}

	values := url.Values{}
	if err := syswMergeSets(values, syswSetParam); err != nil {
		return err
	}
	if len(values) == 0 {
		return output.NewCodedError("USAGE", "nothing to set: provide at least one --set key=value")
	}

	// --set may carry secrets (for example password= or api_key=); mask them
	// from the rendered action label.
	return syswPostValues(cmd, "set system parameters ("+redact.Params(values.Encode())+")", "/api/system/set_param", values)
}

func runSystemSSLCert(cmd *cobra.Command, args []string) error {
	writing := cmd.Flags().Changed("cert-file") || cmd.Flags().Changed("key-file") || syswCertDefault
	if !writing {
		client, err := NewClient(cmd)
		if err != nil {
			return vmDeviceError(err)
		}
		var result map[string]any
		if err := client.Get("/api/system/ssl_cert", &result); err != nil {
			return vmDeviceError(err)
		}
		return syswRenderKV(result)
	}

	if err := vmRequireYes(syswYes, "install the TLS certificate"); err != nil {
		return err
	}

	body := map[string]any{}
	if syswCertDefault {
		body = map[string]any{}
	} else {
		cert, err := syswReadFileOrLiteral(syswCertFile)
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("read certificate: %v", err))
		}
		key, err := syswReadFileOrLiteral(syswKeyFile)
		if err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("read private key: %v", err))
		}
		if strings.TrimSpace(cert) == "" || strings.TrimSpace(key) == "" {
			return output.NewCodedError("USAGE", "both --cert-file and --key-file are required to install a certificate")
		}
		body["ssl_cert"] = cert
		body["ssl_key"] = key
	}

	return syswPostAction(cmd, "install TLS certificate", "/api/system/ssl_cert", body)
}

func runSystemSetTime(cmd *cobra.Command, args []string) error {
	action := "set system time"
	if len(args) == 1 {
		action = fmt.Sprintf("set system time to %s", args[0])
	}
	if err := vmRequireYes(syswYes, action); err != nil {
		return err
	}

	values := url.Values{}
	if len(args) == 1 {
		values.Set("time", strings.TrimSpace(args[0]))
	}
	if err := syswMergeSets(values, syswSetTime); err != nil {
		return err
	}
	if len(values) == 0 {
		return output.NewCodedError("USAGE", "provide a Unix timestamp or --set time=<unix>")
	}

	return syswPostValues(cmd, action, "/api/system/time", values)
}

func runSystemSetTimezone(cmd *cobra.Command, args []string) error {
	tz := strings.TrimSpace(args[0])
	if err := vmRequireYes(syswYes, fmt.Sprintf("set timezone to %q", tz)); err != nil {
		return err
	}
	if tz == "" {
		return output.NewCodedError("USAGE", "timezone must not be empty")
	}

	values := url.Values{"timezone": {tz}}
	if err := syswMergeSets(values, syswSetTimezone); err != nil {
		return err
	}
	return syswPostValues(cmd, "set timezone to "+tz, "/api/system/timezone", values)
}

// ---- shared helpers (cmd package) ---------------------------------
//
// These helpers are prefixed with "sysw" so they do not collide with helpers
// introduced by sibling tickets that share this package.

// syswRenderKV renders a read result as a flattened key/value table, dropping
// the device's top-level "success" marker for consistency with the read-only
// system commands. The remaining object is what JSON/YAML modes emit.
func syswRenderKV(result map[string]any) error {
	data := cliWithout(result, "success")
	td := output.TableData{
		Headers: []string{"KEY", "VALUE"},
		Rows:    vmFlattenRows("", data),
	}
	return output.Render(td, data, GetOutputOptions())
}

// syswPostValues POSTs a query-parameter payload and renders the outcome.
func syswPostValues(cmd *cobra.Command, action, endpoint string, values url.Values) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	path := endpoint
	if enc := values.Encode(); enc != "" {
		path += "?" + enc
	}
	if err := client.Post(path, nil, &result); err != nil {
		return vmDeviceError(err)
	}
	// Defence in depth: never echo a secret-named value the device reflects
	// back in its result.
	return vmRenderAction(action, redact.Map(result))
}

// syswPostAction POSTs a JSON body and renders the outcome.
func syswPostAction(cmd *cobra.Command, action, endpoint string, body map[string]any) error {
	client, err := NewClient(cmd)
	if err != nil {
		return vmDeviceError(err)
	}

	var result map[string]any
	if err := client.Post(endpoint, body, &result); err != nil {
		return vmDeviceError(err)
	}
	return vmRenderAction(action, redact.Map(result))
}

// syswSetIfChanged adds key=value to values only when the user set the flag.
func syswSetIfChanged(cmd *cobra.Command, values url.Values, key string, value *string) {
	if cmd.Flags().Changed(key) {
		values.Set(key, *value)
	}
}

// syswMergeSets parses --set key=value pairs into values, overriding any
// same-named entry already present.
func syswMergeSets(values url.Values, pairs []string) error {
	m, err := syswParseSets(pairs)
	if err != nil {
		return err
	}
	for k, v := range m {
		values.Set(k, v)
	}
	return nil
}

// syswParseSets parses repeated key=value flags into a map.
func syswParseSets(pairs []string) (map[string]string, error) {
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, output.NewCodedError("USAGE", fmt.Sprintf("invalid --set %q (expected key=value)", p))
		}
		m[k] = v
	}
	return m, nil
}

// syswJSONBody builds a JSON object for the write endpoints that take a body.
// When file is non-empty the object is read from that file ("-" means stdin),
// otherwise it starts empty; --set pairs are then merged on top as strings.
func syswJSONBody(file string, pairs []string) (map[string]any, error) {
	body := map[string]any{}
	if strings.TrimSpace(file) != "" {
		var raw []byte
		var err error
		if file == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, output.NewCodedError("USAGE", fmt.Sprintf("read %s: %v", file, err))
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, output.NewCodedError("USAGE", fmt.Sprintf("parse JSON body: %v", err))
		}
	}

	m, err := syswParseSets(pairs)
	if err != nil {
		return nil, err
	}
	for k, v := range m {
		body[k] = v
	}
	return body, nil
}

// syswReadFileOrLiteral returns the contents of path, treating an empty path as an
// empty string. A literal @prefix is not special; only paths are read.
func syswReadFileOrLiteral(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
