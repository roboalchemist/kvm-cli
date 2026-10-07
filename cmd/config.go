package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/auth"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/spf13/cobra"
)

// outputFormatKeys are the aliases accepted for the CLI display preference. The
// value lives alongside the pkg/auth credential config so that a single
// config.json remains the source of truth.
var outputFormatKeys = []string{"output_format", "format"}

// supportedConfigKeys lists every key accepted by 'config set/get/unset'.
func supportedConfigKeys() []string {
	keys := append([]string{}, auth.ConfigKeys...)
	keys = append(keys, "output_format")
	return keys
}

func isOutputFormatKey(key string) bool {
	for _, k := range outputFormatKeys {
		if key == k {
			return true
		}
	}
	return false
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage kvm-cli configuration",
	Long: `Read and write settings in ~/.config/kvm-cli/config.json (mode 0600).

Credentials and connection settings (url, username, password, timeout, insecure)
are read by the shared credential resolver in the priority order:
flags > environment > gopass > config file.

Supported keys:
  - url: KVM device base URL (e.g. https://glkvm.local)
  - username: Username for authentication
  - password: Password for authentication (stored mode 0600)
  - timeout: HTTP request timeout as a Go duration (e.g. 30s, 1m)
  - insecure: Skip TLS certificate verification (true|false)
  - output_format: Default output format (table|json|plaintext|yaml)
  - models_url: Computer-use models platform URL (default https://models.example.com)
  - grounding_model: Screen-parser (OmniParser) model id (default omniparser)
  - planner_model: Element-chooser chat model id (default: auto)
  - scratch_dir: Directory for transient screenshots / Set-of-Mark PNGs (default: OS temp dir)

The output_format key is also accepted under the alias 'format'. Use 'config
unset <key>' to remove a value. Flags always take precedence over configuration
values, which take precedence over built-in defaults.

The models_url, grounding_model and planner_model keys may also be overridden per
invocation by KVM_MODELS_URL, KVM_GROUNDING_MODEL and KVM_PLANNER_MODEL (and
scratch_dir by KVM_SCRATCH_DIR or the global --scratch-dir flag).`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli config list
  kvm-cli config get url
  kvm-cli config set url https://glkvm.local
  kvm-cli config set username admin
  kvm-cli config set timeout 45s
  kvm-cli config set output_format json
  kvm-cli config set scratch_dir /tmp/kvmshot
  kvm-cli config unset password`,
}

var configListCmd = &cobra.Command{
	Use:     "list",
	Short:   "Show all configuration values",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli config list",
	RunE:    runConfigList,
}

var configGetCmd = &cobra.Command{
	Use:     "get KEY",
	Short:   "Show the value of a single config key",
	Long:    "Show the value of a single configuration key, or \"(not set)\" when empty.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli config get url\n  kvm-cli config get output_format",
	RunE:    runConfigGet,
}

var configSetCmd = &cobra.Command{
	Use:     "set KEY VALUE",
	Short:   "Set a config value",
	Long:    "Validate and persist a single configuration value.",
	Args:    cobra.ExactArgs(2),
	Example: "  kvm-cli config set format json\n  kvm-cli config set timeout 45s",
	RunE:    runConfigSet,
}

var configUnsetCmd = &cobra.Command{
	Use:     "unset KEY",
	Short:   "Remove a config value",
	Long:    "Remove a single configuration value, restoring its built-in default.",
	Args:    cobra.ExactArgs(1),
	Example: "  kvm-cli config unset password\n  kvm-cli config unset output_format",
	RunE:    runConfigUnset,
}

func init() {
	MarkWrite(configSetCmd)
	MarkWrite(configUnsetCmd)

	// config set KEY VALUE: the value is secret when KEY names a secret (for
	// example password). The dry-run preview masks it accordingly.
	MarkSecretKV(configSetCmd)

	configCmd.AddCommand(configListCmd, configGetCmd, configSetCmd, configUnsetCmd)
	rootCmd.AddCommand(configCmd)
}

func unknownConfigKeyErr(key string) error {
	return output.NewCodedError("USAGE",
		fmt.Sprintf("unknown config key %q (supported: %s)", key, strings.Join(supportedConfigKeys(), ", ")))
}

// validateConfigValue rejects values that pkg/auth would reject, before the
// write, so the CLI reports a usage error (exit code 2) rather than a
// misclassified runtime error.
func validateConfigValue(key, value string) error {
	switch key {
	case "timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid timeout %q: %v", value, err))
		}
	case "insecure":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on", "0", "false", "no", "off":
		default:
			return output.NewCodedError("USAGE", fmt.Sprintf("invalid insecure %q (expected true or false)", value))
		}
	}
	return nil
}

func runConfigList(cmd *cobra.Command, args []string) error {
	values, err := auth.ListConfig()
	if err != nil {
		return err
	}
	if f := loadOutputFormat(); f != "" {
		values["output_format"] = f
	}

	opts := GetOutputOptions()

	if len(values) == 0 {
		// Keep the friendly human message in the default mode; emit valid
		// (empty) structured output for the machine-readable modes.
		switch opts.Mode {
		case output.ModeJSON, output.ModeYAML:
			return output.Render(output.TableData{}, map[string]string{}, opts)
		case output.ModePlaintext:
			return nil
		default:
			path, _ := auth.ConfigPath()
			fmt.Printf("(no configuration set in %s)\n", path)
			return nil
		}
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([][]string, 0, len(keys))
	masked := make(map[string]string, len(keys))
	for _, k := range keys {
		v := maskConfigValue(k, values[k])
		rows = append(rows, []string{k, v})
		masked[k] = v
	}

	td := output.TableData{Headers: []string{"KEY", "VALUE"}, Rows: rows}
	return output.Render(td, masked, opts)
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	key := args[0]
	opts := GetOutputOptions()

	var value string
	switch {
	case isOutputFormatKey(key):
		value = loadOutputFormat()
	case auth.IsConfigKey(key):
		values, err := auth.ListConfig()
		if err != nil {
			return err
		}
		value = values[key]
	default:
		return unknownConfigKeyErr(key)
	}

	masked := maskConfigValue(key, value)
	// An unset key renders as the historical "(not set)" placeholder in every
	// mode, so structured output stays consistent with the default mode.
	display := masked
	if value == "" {
		display = "(not set)"
	}
	data := map[string]string{key: display}
	// The plaintext renderer joins the row with tabs: "<key>\t<value>".
	td := output.TableData{Rows: [][]string{{key, display}}}

	switch opts.Mode {
	case output.ModeJSON, output.ModeYAML, output.ModePlaintext:
		// JSON/YAML use data; plaintext uses the tab-separated row.
		return output.Render(td, data, opts)
	default:
		// Preserve the historical behaviour: the default mode prints just the
		// value.
		fmt.Println(display)
		return nil
	}
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key, value := args[0], args[1]

	switch {
	case isOutputFormatKey(key):
		if err := validateOutputFormat(value); err != nil {
			return err
		}
		if err := storeOutputFormat(value); err != nil {
			return err
		}
	case auth.IsConfigKey(key):
		if err := validateConfigValue(key, value); err != nil {
			return err
		}
		if err := setAuthConfigValue(key, value); err != nil {
			return err
		}
	default:
		return unknownConfigKeyErr(key)
	}

	path, _ := auth.ConfigPath()
	return renderConfigConfirmation("Set", "set", key, value, path, GetOutputOptions())
}

func runConfigUnset(cmd *cobra.Command, args []string) error {
	key := args[0]

	switch {
	case isOutputFormatKey(key):
		if err := storeOutputFormat(""); err != nil {
			return err
		}
	case auth.IsConfigKey(key):
		if err := unsetAuthConfigValue(key); err != nil {
			return err
		}
	default:
		return unknownConfigKeyErr(key)
	}

	path, _ := auth.ConfigPath()
	return renderConfigConfirmation("Unset", "unset", key, "", path, GetOutputOptions())
}

// maskConfigValue redacts secret configuration values. The password is masked
// in every output mode, and any URL credentials embedded in a non-secret value
// (for example a stored url of the form "https://user:password@host") are
// masked too, so neither 'config get url' nor 'config list' can leak userinfo.
// Ordinary URLs without userinfo are returned unchanged.
func maskConfigValue(key, value string) string {
	if key == "password" && value != "" {
		return redact.Mask
	}
	return redact.Params(value)
}

// renderConfigConfirmation reports the outcome of a config write. In the
// default table mode it prints a one-line confirmation; in the structured modes
// it emits a small action envelope that agents can parse. The value is echoed
// so the caller can confirm what was written, but it is masked first via
// pkg/redact whenever the key names a secret (for example "password").
//
// This envelope describes local configuration metadata, not device data: the
// "key" field is the config key *name* (for example "url"), so the central
// read-path redactor must not mask it. NoRedact opts this render out; the value
// is masked here (not by the renderer) before it is placed in the envelope, and
// 'config list' continues to mask secret values independently.
func renderConfigConfirmation(title, action, key, value, path string, opts output.Options) error {
	// Value masks secret-named keys outright; Params additionally masks
	// credentials embedded in a non-secret value (for example
	// "https://user:password@host") so the real confirmation never echoes them.
	masked := redact.Params(redact.Value(key, value))
	if opts.Mode == output.ModeTable {
		if value == "" {
			fmt.Printf("%s %s in %s\n", title, key, path)
		} else {
			fmt.Printf("%s %s = %s in %s\n", title, key, masked, path)
		}
		return nil
	}
	opts.NoRedact = true
	data := map[string]any{"action": action, "key": key, "path": path}
	headers := []string{"ACTION", "KEY", "PATH"}
	row := []string{action, key, path}
	if value != "" {
		data["value"] = masked
		headers = append(headers, "VALUE")
		row = append(row, masked)
	}
	td := output.TableData{Headers: headers, Rows: [][]string{row}}
	return output.Render(td, data, opts)
}

// setAuthConfigValue persists an auth/config key via pkg/auth, preserving the
// CLI-only output_format preference (which pkg/auth does not model).
func setAuthConfigValue(key, value string) error {
	format := loadOutputFormat()
	if err := auth.SetConfigValue(key, value); err != nil {
		return err
	}
	return restoreOutputFormat(format)
}

// unsetAuthConfigValue clears an auth/config key via pkg/auth, preserving the
// CLI-only output_format preference.
func unsetAuthConfigValue(key string) error {
	format := loadOutputFormat()

	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	switch key {
	case "url":
		cfg.URL = ""
	case "username":
		cfg.Username = ""
	case "password":
		cfg.Password = ""
	case "timeout":
		cfg.Timeout = ""
	case "insecure":
		cfg.Insecure = false
	case "models_url":
		cfg.ModelsURL = ""
	case "grounding_model":
		cfg.GroundingModel = ""
	case "planner_model":
		cfg.PlannerModel = ""
	case "scratch_dir":
		cfg.ScratchDir = ""
	}
	if err := auth.SaveConfig(cfg); err != nil {
		return err
	}
	return restoreOutputFormat(format)
}

// restoreOutputFormat re-applies the output_format preference after a write
// performed through pkg/auth (which drops fields it does not model).
func restoreOutputFormat(format string) error {
	if strings.TrimSpace(format) == "" {
		return nil
	}
	return storeOutputFormat(format)
}

// loadOutputFormat reads the persisted output_format preference. A missing or
// malformed file yields an empty string; callers treat that as "unset".
func loadOutputFormat() string {
	path, err := auth.ConfigPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	for _, k := range outputFormatKeys {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if v := strings.TrimSpace(s); v != "" {
				return v
			}
		}
	}
	return ""
}

// storeOutputFormat persists (or clears, when value is empty) the output_format
// preference while preserving every field managed by pkg/auth.
func storeOutputFormat(value string) error {
	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	path, err := auth.ConfigPath()
	if err != nil {
		return err
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	obj := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("encode config: %w", err)
		}
	}

	if value = strings.TrimSpace(value); value == "" {
		delete(obj, "output_format")
	} else {
		obj["output_format"] = value
	}

	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// validateOutputFormat rejects output_format values that pkg/output cannot
// interpret.
func validateOutputFormat(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "table", "json", "plaintext", "plain", "yaml", "yml":
		return nil
	default:
		return output.NewCodedError("USAGE", fmt.Sprintf("invalid output_format %q (expected table|json|plaintext|yaml)", v))
	}
}
