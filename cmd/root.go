package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/auth"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/roboalchemist/kvm-cli/pkg/scratch"
	"github.com/spf13/cobra"
)

// errStopSuccess signals that a global short-circuit (--version or --dry-run)
// fully handled the invocation and Execute must exit 0 without running the
// command. It is consumed by Execute and never surfaced to the user.
var errStopSuccess = errors.New("kvm-cli: handled without executing")

// writeAnnotation marks a command as a write/destructive operation. It is set
// by MarkWrite and consulted by the central --dry-run enforcement.
const writeAnnotation = "write"

// secretArgsAnnotation lists positional argument indices (comma-separated) whose
// values are secrets, so the --dry-run preview can mask them. It is set by
// MarkSecretArgs.
const secretArgsAnnotation = "secret_args"

// secretKVAnnotation marks a command whose first two positional arguments are a
// KEY VALUE pair where the value is secret whenever KEY names a secret (for
// example 'config set password <value>'). It is set by MarkSecretKV.
const secretKVAnnotation = "secret_kv"

// MarkWrite records that cmd mutates device or locally persisted state.
// PersistentPreRunE honours the global --dry-run flag for any command so
// annotated: it prints a preview of the would-be action and exits 0 without
// contacting the device.
func MarkWrite(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[writeAnnotation] = "true"
}

// MarkSecretArgs records that the given positional argument indices carry
// secret values (for example 'zerotier set-token TOKEN' or 'modem input-pin
// PIN'). The values are masked in the --dry-run preview and should otherwise
// never be echoed.
func MarkSecretArgs(cmd *cobra.Command, indices ...int) {
	if len(indices) == 0 {
		return
	}
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	parts := make([]string, 0, len(indices))
	for _, i := range indices {
		parts = append(parts, strconv.Itoa(i))
	}
	cmd.Annotations[secretArgsAnnotation] = strings.Join(parts, ",")
}

// MarkSecretKV records that cmd takes a KEY VALUE positional pair whose value
// is secret whenever KEY names a secret (see redact.IsSecretKey). The value is
// masked in the --dry-run preview. It complements MarkSecretArgs, which pins
// fixed positional indices; config set's key is only known at run time.
func MarkSecretKV(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[secretKVAnnotation] = "true"
}

// isSecretKVCommand reports whether cmd's KEY VALUE positional pair is
// conditionally secret.
func isSecretKVCommand(cmd *cobra.Command) bool {
	return cmd.Annotations[secretKVAnnotation] == "true"
}

// secretArgSet returns the set of positional indices marked secret for cmd.
func secretArgSet(cmd *cobra.Command) map[int]bool {
	raw := cmd.Annotations[secretArgsAnnotation]
	if raw == "" {
		return nil
	}
	out := map[int]bool{}
	for _, p := range strings.Split(raw, ",") {
		if i, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out[i] = true
		}
	}
	return out
}

// isWriteCommand reports whether cmd was annotated as a write command.
func isWriteCommand(cmd *cobra.Command) bool {
	return cmd.Annotations[writeAnnotation] == "true"
}

// versionRequested reports whether argv contains the global --version/-V flag.
// It stops at the "--" terminator and defers to --help (cobra gives help
// precedence), so "kvm-cli cmd -- --version" and "--help --version" behave as
// before.
func versionRequested(argv []string) bool {
	requested := false
	for _, a := range argv {
		switch a {
		case "--":
			return requested
		case "-h", "--help":
			return false
		case "-V", "--version", "--version=true", "-V=true":
			requested = true
		case "--version=false", "-V=false":
			requested = false
		}
	}
	return requested
}

// writeVersionBlock prints the GNU §4.8.1 version block. It reuses the root
// command's version template so the root and subcommand forms stay in sync.
func writeVersionBlock(w io.Writer) {
	tmpl := rootCmd.VersionTemplate()
	t, err := template.New("version").Parse(tmpl)
	if err != nil {
		fmt.Fprintf(w, "%s %s\n", rootCmd.Name(), appVersion)
		return
	}
	if err := t.Execute(w, rootCmd); err != nil {
		fmt.Fprintf(w, "%s %s\n", rootCmd.Name(), appVersion)
	}
}

// validateFormatFlag rejects an unknown --format value with a USAGE error. The
// explicit --json/--plaintext shorthands win, so the format value is irrelevant
// (and unchecked) when either is set.
func validateFormatFlag() error {
	if flagJSON || flagPlaintext {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(flagFormat)) {
	case "", "table", "json", "plaintext", "plain", "yaml", "yml":
		return nil
	default:
		return output.NewCodedError("USAGE",
			fmt.Sprintf("invalid value %q for --format: expected table, json, plaintext, or yaml", flagFormat))
	}
}

// renderDryRunPreview prints a preview of the write command that --dry-run
// suppressed, honouring the global output flags. The preview is emitted on
// stdout and Execute exits 0, so it is clearly distinct from the
// CONFIRMATION_REQUIRED error emitted when a destructive command is run without
// --yes or -f/--force.
func renderDryRunPreview(cmd *cobra.Command, args []string) error {
	action := cmd.CommandPath()
	if len(args) > 0 {
		action += " " + strings.Join(maskPreviewArgs(cmd, args), " ")
	}
	msg := fmt.Sprintf("dry run: would run %q; re-run without --dry-run to execute", action)
	data := map[string]any{"dry_run": true, "action": action, "message": msg}
	td := output.TableData{
		Headers: []string{"ACTION"},
		Rows:    [][]string{{msg}},
	}
	return output.Render(td, data, GetOutputOptions())
}

// maskPreviewArgs renders the positional arguments for a --dry-run preview with
// every secret masked. Arguments explicitly marked secret via MarkSecretArgs are
// replaced wholesale; for a MarkSecretKV command (config set KEY VALUE) the
// value is masked when the key names a secret (for example "password"); any
// remaining key=value argument has its value masked when the key names a secret
// (for example "password=x"). URL credentials (user:password@host) are masked
// by redact.Params.
func maskPreviewArgs(cmd *cobra.Command, args []string) []string {
	secret := secretArgSet(cmd)
	kv := isSecretKVCommand(cmd)
	out := make([]string, len(args))
	for i, a := range args {
		if secret[i] {
			out[i] = redact.Mask
			continue
		}
		if kv && i == 1 && len(args) >= 2 && redact.IsSecretKey(args[0]) {
			out[i] = redact.Mask
			continue
		}
		out[i] = redact.Params(a)
	}
	return out
}

// appVersion is the application version, set via SetVersion (ldflags -> main.version).
var appVersion = "dev"

// Global (persistent) flag values.
var (
	flagURL        string
	flagUsername   string
	flagPassword   string
	flagFormat     string
	flagJSON       bool
	flagPlaintext  bool
	flagNoColor    bool
	flagDebug      bool
	flagVerbose    bool
	flagQuiet      bool
	flagSilent     bool
	flagDryRun     bool
	flagFields     string
	flagJQ         string
	flagConfig     string
	flagTimeout    time.Duration
	flagInsecure   bool
	flagVersion    bool
	flagScratchDir string
)

const (
	projectHome = "https://github.com/roboalchemist/kvm-cli"
	issuesURL   = projectHome + "/issues"
	// helpFooter is appended to every --help output to satisfy GNU §4.8.2.
	helpFooter = "\nReport bugs to: " + issuesURL + "\nHome page: " + projectHome
)

var rootCmd = &cobra.Command{
	Use:   "kvm-cli",
	Short: "Agent-first CLI for the GL.iNet Comet PoE Remote KVM (GL-RM1PE)",
	Long: `kvm-cli is an agent-first command-line interface for controlling a computer through
the GL.iNet Comet PoE Remote KVM (GL-RM1PE). The device is PiKVM-based (kvmd under the
hood) and exposes a REST API, WebSocket HID control, and a JPEG snapshot endpoint.

The primary consumers are computer-use agents, so screenshot capture, keyboard/mouse
injection, and ATX power control are first-class, scriptable, and JSON-addressable.

Authentication:
  Provide --url, --username, and --password flags, or set them via environment
  variables or ~/.config/kvm-cli/config.json (see 'kvm-cli config --help').

Output:
  By default results are rendered as a table for humans. Use --json (-j) for
  machine-readable JSON, --plaintext (-p) for tab-separated piping, or
  --format table|json|plaintext|yaml. In JSON mode, --fields projects to a subset
  of keys and --jq filters with a jq expression.

Environment Variables:
  - KVM_URL: KVM device base URL (alias: GLKVM_URL)
  - KVM_USERNAME: Username for authentication (alias: GLKVM_USERNAME)
  - KVM_PASSWORD: Password for authentication (alias: GLKVM_PASSWORD)
  - KVM_TIMEOUT: HTTP request timeout (Go duration, e.g. 30s)
  - KVM_INSECURE: Skip TLS verification (true|false)
  - KVM_CONFIG: Override config file path (alias: GLKVM_CONFIG)
  - KVM_SCRATCH_DIR: Directory for transient screenshots/artifacts (default: OS temp dir)
  - KVM_MODELS_URL: Computer-use models platform URL (default https://models.example.com)
  - KVM_GROUNDING_MODEL: Screen-parser (OmniParser) model id (default omniparser)
  - KVM_PLANNER_MODEL: Element-chooser chat model id (default: auto)
  - NO_COLOR: Disable colored output when set to any value

Files:
  ~/.config/kvm-cli/config.json   Persisted configuration (mode 0600)

Exit Status:
  0   Success
  1   User or correctable error (bad arguments, missing config, resource not found)
  2   Usage error (incorrect invocation)
  3   System or runtime error (network failure, device/server error)

Report bugs to: ` + issuesURL + `
Home page: ` + projectHome,
	Example: `  # Point at a device and read system info as JSON
  kvm-cli --url https://glkvm.local --username admin --password secret info --json

  # Capture a screenshot to a file
  kvm-cli screenshot --output screen.jpg

  # Power the target on
  kvm-cli atx power

  # Force a power-off (hold the power button for 5 seconds)
  kvm-cli atx power --long

  # Persist credentials for future invocations
  kvm-cli config set url https://glkvm.local
  kvm-cli config set username admin`,
	Version:       appVersion,
	SilenceUsage:  true,
	SilenceErrors: true,
	// Args is intentionally left nil: cobra's legacy arg handling is what maps
	// an unrecognized positional argument to an "unknown command" error, which
	// Execute maps to exit code 2.
}

func init() {
	// Assigned here (not in the struct literal) to avoid an initialization cycle.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// --version/-V must short-circuit ANY command before it runs. Cobra
		// only honours the version flag on the root command, so a persistent
		// flag on a subcommand would otherwise fall through and execute it.
		if flagVersion {
			writeVersionBlock(cmd.OutOrStdout())
			return errStopSuccess
		}

		applyGlobalConfig(cmd)

		if err := validateFormatFlag(); err != nil {
			return err
		}

		// --dry-run is enforced centrally: every command annotated as a write
		// previews its would-be action and exits 0 without contacting the
		// device, even when --yes/-f/--force is also supplied.
		if flagDryRun && isWriteCommand(cmd) {
			if err := renderDryRunPreview(cmd, args); err != nil {
				return err
			}
			return errStopSuccess
		}

		Verbosef("running %s", cmd.CommandPath())
		return nil
	}

	pf := rootCmd.PersistentFlags()
	pf.StringVar(&flagURL, "url", "", "KVM device base URL (env: KVM_URL, GLKVM_URL)")
	pf.StringVar(&flagUsername, "username", "", "Username for authentication (env: KVM_USERNAME, GLKVM_USERNAME)")
	pf.StringVar(&flagPassword, "password", "", "Password for authentication (env: KVM_PASSWORD, GLKVM_PASSWORD)")
	pf.StringVar(&flagFormat, "format", "table", "Output format: table|json|plaintext|yaml")
	pf.BoolVarP(&flagJSON, "json", "j", false, "JSON output (shorthand for --format json)")
	pf.BoolVarP(&flagPlaintext, "plaintext", "p", false, "Tab-separated output for piping (shorthand for --format plaintext)")
	pf.BoolVar(&flagNoColor, "no-color", false, "Disable colored output (also honours NO_COLOR)")
	pf.BoolVar(&flagDebug, "debug", false, "Verbose debug logging to stderr")
	// GNU §4.8 standard global flags. -v is reserved for --verbose; the
	// version short flag stays -V (see below).
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "Print more information about progress to stderr")
	pf.BoolVarP(&flagQuiet, "quiet", "q", false, "Suppress non-error output on stderr")
	pf.BoolVar(&flagSilent, "silent", false, "Suppress non-error output on stderr (synonym for --quiet)")
	pf.BoolVar(&flagDryRun, "dry-run", false, "Simulate write/destructive commands without executing them")
	pf.StringVar(&flagFields, "fields", "", "Comma-separated fields to include in JSON output")
	pf.StringVar(&flagJQ, "jq", "", "jq expression to filter JSON output")
	pf.StringVar(&flagConfig, "config", "", "Config file path (default: ~/.config/kvm-cli/config.json)")
	pf.DurationVar(&flagTimeout, "timeout", 30*time.Second, "HTTP request timeout")
	pf.BoolVar(&flagInsecure, "insecure", false, "Skip TLS certificate verification")
	pf.StringVar(&flagScratchDir, "scratch-dir", "", "Directory for transient screenshots/artifacts (env: KVM_SCRATCH_DIR)")

	// GNU §4.8.1: --version uses -V (capital); -v is reserved for --verbose.
	// Bind the flag to flagVersion so PersistentPreRunE can honour it on any
	// subcommand, and keep rootCmd.Version/template so the root form also works.
	rootCmd.PersistentFlags().BoolVarP(&flagVersion, "version", "V", false, "version for kvm-cli")

	// GNU §4.8.1 --version output format.
	rootCmd.SetVersionTemplate(`{{.Name}} {{.Version}}
Copyright © 2024 roboalchemist
License MIT: <https://opensource.org/licenses/MIT>
`)

	// GNU §4.8.2 --help must end with the bug-report URL and home page.
	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + helpFooter + "\n")

	// --help short-circuits before PersistentPreRunE runs, so emit the verbose
	// progress line from a help wrapper to keep --verbose observable there too.
	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		Verbosef("help for %s", c.CommandPath())
		defaultHelp(c, args)
	})
}

// applyGlobalConfig bridges persistent flags into the environment that pkg/auth
// consumes and applies a persisted output_format as the default format. It is
// intentionally best-effort: built-in commands (docs, version, completion,
// skill, man) must keep working even with no or malformed configuration.
func applyGlobalConfig(cmd *cobra.Command) {
	flags := cmd.Flags()

	// pkg/auth reads the config path from KVM_CONFIG/GLKVM_CONFIG, so bridge
	// --config rather than duplicating config resolution here.
	if flags.Changed("config") && flagConfig != "" {
		_ = os.Setenv("KVM_CONFIG", flagConfig)
	}
	// pkg/auth resolves timeout/insecure from KVM_TIMEOUT/KVM_INSECURE; bridge
	// the flags so that an explicit flag still wins over config/gopass.
	if flags.Changed("timeout") {
		_ = os.Setenv("KVM_TIMEOUT", flagTimeout.String())
	}
	if flags.Changed("insecure") {
		_ = os.Setenv("KVM_INSECURE", strconv.FormatBool(flagInsecure))
	}

	// A persisted output_format acts as the default only when the user did not
	// choose a format explicitly on the command line.
	if !flags.Changed("format") && !flags.Changed("json") && !flags.Changed("plaintext") {
		if f := loadOutputFormat(); f != "" {
			flagFormat = f
		}
	}
}

// GetOutputOptions builds output.Options from the global flags. The explicit
// --json / --plaintext shorthands take precedence over --format.
func GetOutputOptions() output.Options {
	opts := output.Options{
		NoColor: flagNoColor,
		Debug:   flagDebug,
		JQ:      flagJQ,
	}

	switch {
	case flagJSON:
		opts.Mode = output.ModeJSON
	case flagPlaintext:
		opts.Mode = output.ModePlaintext
	default:
		opts.Mode = output.ParseMode(flagFormat)
	}

	if fields := strings.TrimSpace(flagFields); fields != "" {
		for _, f := range strings.Split(fields, ",") {
			if f = strings.TrimSpace(f); f != "" {
				opts.Fields = append(opts.Fields, f)
			}
		}
	}
	return opts
}

// AuthFlagValues returns the credential flag values the user explicitly set on
// the command line, with unset flags reported as empty strings. Callers pass
// the result to pkg/auth, which applies the remaining layers (env > gopass >
// config > prompt) only for the fields the user did not supply.
func AuthFlagValues(cmd *cobra.Command) (url, username, password string) {
	flags := cmd.Flags()
	if flags.Changed("url") {
		url = flagURL
	}
	if flags.Changed("username") {
		username = flagUsername
	}
	if flags.Changed("password") {
		password = flagPassword
	}
	return url, username, password
}

// NewClient resolves credentials (flags > env > gopass > config) and returns an
// authenticated API client. Only flags the user actually set are forwarded so
// that pkg/auth can apply the rest of the credential chain.
func NewClient(cmd *cobra.Command) (*api.Client, error) {
	url, username, password := AuthFlagValues(cmd)
	client, err := getAuthClient(url, username, password)
	if err != nil {
		return nil, err
	}
	if flagDebug {
		client.SetDebug(DebugLog)
	}
	if flagVerbose {
		Verbosef("authenticated with the device")
	}
	return client, nil
}

// getAuthClient performs credential resolution/authentication. Under
// --quiet/--silent it suppresses the informational TLS-fallback warning that
// pkg/auth writes directly to stderr; --verbose wins over --quiet so the
// warning remains visible when it is requested.
func getAuthClient(url, username, password string) (*api.Client, error) {
	return withQuietStderr(func() (*api.Client, error) {
		return auth.GetClientFromFlags(url, username, password)
	})
}

// getAuthClientFromCreds builds a client from already-resolved credentials
// using the same quiet-aware stderr handling as getAuthClient. It exists for
// commands (auth status/login) that resolve credentials themselves so they can
// report the URL/username even when authentication is impossible.
func getAuthClientFromCreds(creds auth.Credentials) (*api.Client, error) {
	return withQuietStderr(func() (*api.Client, error) {
		return auth.GetClient(creds)
	})
}

// withQuietStderr runs fn with os.Stderr redirected to /dev/null when
// --quiet/--silent is in effect. Errors are unaffected; only informational
// stderr output (such as pkg/auth's TLS-fallback warning) is suppressed. If the
// redirect cannot be set up, fn runs unmodified rather than failing.
func withQuietStderr(fn func() (*api.Client, error)) (*api.Client, error) {
	if !quietMode() {
		return fn()
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fn()
	}
	orig := os.Stderr
	os.Stderr = devNull
	client, fnErr := fn()
	os.Stderr = orig
	_ = devNull.Close()
	return client, fnErr
}

// quietMode reports whether non-error stderr output should be suppressed.
// --verbose overrides --quiet/--silent so that requested progress is shown.
func quietMode() bool {
	return (flagQuiet || flagSilent) && !flagVerbose
}

// Verbosef writes a progress line to stderr when --verbose is set.
func Verbosef(format string, args ...interface{}) {
	if !flagVerbose {
		return
	}
	fmt.Fprintf(os.Stderr, "kvm-cli: "+format+"\n", args...)
}

// GetRootCmd returns the root command (used by cmd/gendocs).
func GetRootCmd() *cobra.Command {
	return rootCmd
}

// ScratchDir resolves the effective scratch directory for transient artifacts
// (screenshots, Set-of-Mark PNGs) using the precedence
// --scratch-dir flag > $KVM_SCRATCH_DIR > config scratch_dir > os.TempDir().
// The directory is created (mode 0700) when missing. A malformed config is
// ignored rather than fatal, so built-in commands keep working.
func ScratchDir() (string, error) {
	configured := ""
	if cfg, err := auth.LoadConfig(); err == nil && cfg != nil {
		configured = cfg.ScratchDir
	}
	return scratch.Resolve(flagScratchDir, configured)
}

// Execute runs the root command and maps errors to process exit codes:
//
//	0 success
//	1 user or correctable error
//	2 usage error (unknown flag/command, invalid arguments)
//	3 system or runtime error (network failure, device/server error)
func Execute() error {
	// Honour --version/-V for every command before cobra dispatches. This
	// covers subcommands with required positional arguments, for which cobra
	// validates args before PersistentPreRunE could short-circuit.
	if versionRequested(os.Args[1:]) {
		writeVersionBlock(os.Stdout)
		return nil
	}

	// Make non-runnable group commands reject unknown subcommands with a
	// structured error instead of cobra's default (help text + exit 0).
	applyGroupCommandGuards()

	rootCmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return output.WrapCodedError("USAGE", err, err.Error())
	})

	err := rootCmd.Execute()
	if err == nil {
		return nil
	}
	// A global short-circuit (--version or --dry-run) already produced its
	// output and must exit 0 without rendering an error.
	if errors.Is(err, errStopSuccess) {
		return nil
	}

	renderExecuteError(err, os.Args[1:])
	os.Exit(exitCode(err))
	return err
}

// renderExecuteError writes a failed root-command invocation to stderr. When
// cobra could not parse the global flags (for example because --json appeared
// after an unknown command or flag), it falls back to scanning argv so JSON
// consumers still get the structured {"error":{...}} envelope.
func renderExecuteError(err error, argv []string) {
	opts := GetOutputOptions()
	if opts.Mode != output.ModeJSON && jsonFlagRequested(argv) {
		opts.Mode = output.ModeJSON
	}
	_ = output.RenderError(err, opts)
}

// jsonFlagRequested reports whether argv contains the global --json/-j flag. It
// is used to render errors that occur before cobra parses the flag (an unknown
// command or flag preceding --json), and mirrors versionRequested's handling of
// the "--" terminator and --help precedence.
func jsonFlagRequested(argv []string) bool {
	requested := false
	for _, a := range argv {
		switch a {
		case "--":
			return requested
		case "-h", "--help":
			return false
		case "-j", "--json", "--json=true", "-j=true":
			requested = true
		case "--json=false", "-j=false":
			requested = false
		}
	}
	return requested
}

// groupGuardsApplied ensures the group-command guards are installed once.
var groupGuardsApplied bool

// applyGroupCommandGuards makes every grouping command that has subcommands but
// no Run of its own return a structured USAGE error for an unrecognized
// subcommand, rather than cobra's default behaviour of printing help and exiting
// 0. Invoking such a command with no arguments still prints help and exits 0.
//
// The root command is deliberately left untouched so its richer
// "unknown command ... Did you mean ...?" suggestions are preserved.
func applyGroupCommandGuards() {
	if groupGuardsApplied {
		return
	}
	groupGuardsApplied = true

	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c.HasSubCommands() && c.HasParent() && !c.Runnable() {
			c.Args = cobra.ArbitraryArgs
			c.RunE = func(cc *cobra.Command, args []string) error {
				if len(args) == 0 {
					return cc.Help()
				}
				return output.NewCodedError("USAGE",
					fmt.Sprintf("unknown command %q for %q", args[0], cc.CommandPath()))
			}
		}
	}
	walk(rootCmd)
}

// exitCode maps err to a process exit code. Usage errors are 2; network and
// device/server failures are 3 (GNU: system/runtime error); everything else is
// a user or correctable error and maps to 1.
func exitCode(err error) int {
	switch output.ErrorCode(err) {
	case "USAGE":
		return 2
	case "DEVICE_ERROR", "NETWORK_ERROR":
		return 3
	default:
		return 1
	}
}

// SetVersion sets the application version (called from main with the ldflags value).
func SetVersion(v string) {
	appVersion = v
	rootCmd.Version = v
}

// GetVersion returns the current application version.
func GetVersion() string {
	return appVersion
}

// DebugLog prints debug output to stderr when --debug is set.
func DebugLog(format string, args ...interface{}) {
	if flagDebug {
		fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
	}
}
