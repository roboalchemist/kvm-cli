# kvm-cli configuration

`kvm-cli` resolves its connection settings (URL, username, password, timeout,
TLS behaviour) from a layered chain and stores persisted values in a single
JSON file. This document describes the file schema, every environment variable,
the credential priority chain, and the built-in defaults.

It also covers the four non-secret **computer-use assistance (CUA)** keys
(`models_url`, `grounding_model`, `planner_model`, `scratch_dir`) used by
`kvm-cli cua` and the screenshot scratch-directory default.

## File location

| Path | Purpose | Permissions |
|------|---------|-------------|
| `~/.config/kvm-cli/config.json` | Persisted configuration (credentials + preferences) | file `0600`, directory `0700` |

Override the path with the global `--config <path>` flag or the `KVM_CONFIG`
environment variable (alias `GLKVM_CONFIG`).

## Credential priority chain

Each credential field is resolved independently, from highest priority to
lowest. The first layer that supplies a non-empty value wins:

1. **Command-line flags** — `--url`, `--username`, `--password`
2. **Primary environment variable** — `KVM_URL`, `KVM_USERNAME`, `KVM_PASSWORD`
3. **Alias environment variable** — `GLKVM_URL`, `GLKVM_USERNAME`, `GLKVM_PASSWORD`
4. **gopass** — `env/GLKVM_URL`, `env/GLKVM_USERNAME`, `env/GLKVM_PASSWORD`
5. **Config file** — `~/.config/kvm-cli/config.json`
6. **Interactive prompt** — only when stdin is a TTY and a value is still missing

`timeout` and `insecure` follow a shorter chain: **flag > env > gopass > config
> built-in default**. The `--timeout` and `--insecure` flags bridge into the
`KVM_TIMEOUT` / `KVM_INSECURE` environment variables before the resolver runs,
so an explicit flag always wins.

The four **CUA** keys (`models_url`, `grounding_model`, `planner_model`,
`scratch_dir`) follow the same short chain:
**flag > env > config > built-in default**.

A missing value is not itself an error; the first network command reports which
field is missing and lists every way to supply it.

## `config.json` schema

The file is plain JSON (comments shown below are for documentation only). Only
`url`, `username`, `password`, `timeout`, `insecure`, `output_format`, and the
four CUA keys (`models_url`, `grounding_model`, `planner_model`, `scratch_dir`)
are recognised; unknown keys are ignored.

```jsonc
{
  // KVM device base URL, including scheme. Required.
  // Example: "https://glkvm.local"
  "url": "https://glkvm.local",

  // Username for authentication. Required.
  "username": "admin",

  // Password for authentication. Stored in plaintext; the file is mode 0600.
  // Prefer gopass/env vars over the config file for real credentials.
  "password": "secret",

  // HTTP request timeout as a Go duration string ("30s", "1m", "500ms").
  // Omitted or empty => the built-in default (30s).
  "timeout": "30s",

  // Skip TLS certificate verification.
  // Accepts a JSON boolean or the strings "true"/"false"/"1"/"0".
  // Omit (or set false) to let kvm-cli verify and auto-fall back to insecure.
  "insecure": false,

  // Default output format used when no --format/--json/--plaintext flag is
  // given. CLI-only preference; also accepted under the key "format".
  // One of: table | json | plaintext | yaml
  "output_format": "table",

  // --- Computer-use assistance (CUA) keys (non-secret) ---------------------

  // Models platform root used by 'kvm-cli cua'. Empty => the built-in default
  // "https://models.example.com".
  "models_url": "https://models.example.com",

  // Grounding (screen-parser, OmniParser) model id for 'cua ground'/'cua click'.
  // Empty => the built-in default "omniparser".
  "grounding_model": "omniparser",

  // Planner (element-chooser) chat model id for 'cua click'. Empty => "auto":
  // pick a running chat model from the platform catalog.
  "planner_model": "hemmingway",

  // Directory for transient artifacts (screenshots, Set-of-Mark PNGs). Empty =>
  // the OS temp directory. Also settable per-invocation with --scratch-dir.
  "scratch_dir": "/tmp/kvmshot"
}
```

### Field reference

| Key | Type | Default | Managed by | Description |
|-----|------|---------|-----------|-------------|
| `url` | string | *(none)* | `config set url` | Device base URL, e.g. `https://glkvm.local` |
| `username` | string | *(none)* | `config set username` | Login username (the device user is usually `admin`) |
| `password` | string | *(none)* | `config set password` | Login password (plaintext, mode `0600`) |
| `timeout` | string (duration) | `"30s"` | `config set timeout` | Per-request HTTP timeout |
| `insecure` | bool or string | `false` | `config set insecure` | Skip TLS verification |
| `output_format` | string | `"table"` | `config set output_format` | Default render mode |
| `models_url` | string | `"https://models.example.com"` | `config set models_url` | CUA models-platform root |
| `grounding_model` | string | `"omniparser"` | `config set grounding_model` | OmniParser grounding (screen-parser) model id |
| `planner_model` | string | *(auto)* | `config set planner_model` | CUA planner (element-chooser) chat model id; empty picks a running chat model |
| `scratch_dir` | string | OS temp dir | `config set scratch_dir` | Directory for transient screenshots / Set-of-Mark PNGs |

`config list` omits unset keys and `insecure=false`. `config unset <key>`
removes a key and restores its built-in default.

## Environment variables

| Variable | Alias | Overrides | Example |
|----------|-------|-----------|---------|
| `KVM_URL` | `GLKVM_URL` | `url` | `https://glkvm.local` |
| `KVM_USERNAME` | `GLKVM_USERNAME` | `username` | `admin` |
| `KVM_PASSWORD` | `GLKVM_PASSWORD` | `password` | `hunter2` |
| `KVM_TIMEOUT` | `GLKVM_TIMEOUT` | `timeout` | `45s` |
| `KVM_INSECURE` | `GLKVM_INSECURE` | `insecure` | `true` |
| `KVM_TLS_STRICT` | `GLKVM_TLS_STRICT` | TLS fallback policy | `1` |
| `KVM_CONFIG` | `GLKVM_CONFIG` | config file path | `/etc/kvm-cli.json` |
| `KVM_MODELS_URL` | — | `models_url` | `https://models.example.com` |
| `KVM_GROUNDING_MODEL` | — | `grounding_model` | `omniparser` |
| `KVM_PLANNER_MODEL` | — | `planner_model` | `hemmingway` |
| `KVM_SCRATCH_DIR` | — | `scratch_dir` | `/tmp/kvmshot` |
| `NO_COLOR` | — | colour output | any value |

### Notes

- **`KVM_TLS_STRICT`** — by default `kvm-cli` attempts certificate verification
  and, only if it fails with a certificate error, prints a warning and retries
  with insecure TLS (the GL-RM1PE ships a self-signed certificate with a
  1970–1979 validity window, so verification always fails). Set
  `KVM_TLS_STRICT=1` to disable the fallback and require a valid certificate.
  Pass `--insecure` to skip the initial verification attempt entirely.
- **`KVM_CONFIG`** vs `--config` — both point at the config file. The `--config`
  flag is bridged into `KVM_CONFIG` before resolution, so the flag wins.
- **gopass** — when the `gopass` binary is available, `kvm-cli` also looks up
  `env/GLKVM_URL`, `env/GLKVM_USERNAME`, `env/GLKVM_PASSWORD`,
  `env/GLKVM_TIMEOUT`, and `env/GLKVM_INSECURE`. Lookups are silent and bounded
  by a 10-second timeout; a locked or missing store is ignored.
- **CUA keys** — `KVM_MODELS_URL`, `KVM_GROUNDING_MODEL` and
  `KVM_PLANNER_MODEL` override the matching `kvm-cli cua` config keys;
  `KVM_SCRATCH_DIR` sets the scratch directory for `cua` and for the
  `screenshot` / `streamer snapshot` default output path. These are resolved as
  **flag > env > config > default**, so an explicit `--models-url`, `--model`,
  `--planner` or `--scratch-dir` always wins. See the CUA section in
  [../README.md](../README.md) and [../skill/SKILL.md](../skill/SKILL.md).

## Defaults

| Setting | Default | Override |
|---------|---------|----------|
| Output format | `table` | `--format`, `--json`/`-j`, `--plaintext`/`-p`, `output_format` |
| Request timeout | `30s` | `--timeout`, `KVM_TIMEOUT`, gopass, `timeout` |
| TLS verification | verify, then auto-fallback | `--insecure`, `KVM_INSECURE`, `KVM_TLS_STRICT` |
| Colour | auto (TTY-aware) | `--no-color`, `NO_COLOR` |
| CUA models platform | `https://models.example.com` | `--models-url`, `KVM_MODELS_URL`, `models_url` |
| CUA grounding model | `omniparser` | `--model`, `KVM_GROUNDING_MODEL`, `grounding_model` |
| CUA planner model | auto (a running chat model) | `--planner`, `KVM_PLANNER_MODEL`, `planner_model` |
| Scratch directory | OS temp dir | `--scratch-dir`, `KVM_SCRATCH_DIR`, `scratch_dir` |

## Managing configuration

```bash
kvm-cli config list                                  # show all persisted values
kvm-cli config get url                               # show one value
kvm-cli config set url https://glkvm.local
kvm-cli config set username admin
kvm-cli config set timeout 45s
kvm-cli config set output_format json
kvm-cli config set models_url https://models.example.com   # CUA platform root
kvm-cli config set grounding_model omniparser               # CUA screen parser
kvm-cli config set planner_model hemmingway                 # CUA element chooser
kvm-cli config set scratch_dir /tmp/kvmshot                 # transient artifacts
kvm-cli config unset password                        # remove a value
```

`config set` validates `timeout` (Go duration) and `output_format`
(`table|json|plaintext|yaml`) before writing and returns exit code `2` on an
invalid value.

## Security

- The config file is written with mode `0600` and its directory with `0700`.
- Passwords are stored in plaintext when set via `config set password`. Prefer
  gopass or environment variables for real credentials; use the config file for
  non-secret settings or a device-specific convenience password.
- The password is never printed by `auth status` or any other command.
- The CUA keys (`models_url`, `grounding_model`, `planner_model`,
  `scratch_dir`) are non-secret and safe to store in the config file.
