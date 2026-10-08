# kvm-cli

Agent-first command-line interface for the **GL.iNet Comet PoE Remote KVM
(GL-RM1PE)** — a PiKVM-based remote KVM that exposes a REST API, a WebSocket HID
channel, and a JPEG snapshot endpoint.

`kvm-cli` drives a computer over the Comet: capture screenshots, inject
keyboard/mouse input, mount virtual media, wake machines over LAN, and control
ATX power. It is designed for **computer-use agents**, so screenshot capture,
HID injection, and power control are first-class, scriptable, and
JSON-addressable. It also ships a built-in computer-use assistance (`cua`) flow
that grounds a screenshot into numbered UI elements and resolves a
natural-language instruction to a click via an external models platform.

## Installation

### Homebrew

```bash
brew tap roboalchemist/tap
brew install kvm-cli
```

### Go

```bash
go install github.com/roboalchemist/kvm-cli@latest
```

### From source

```bash
git clone https://github.com/roboalchemist/kvm-cli.git
cd kvm-cli
make build          # produces ./kvm-cli
make install        # installs to /usr/local/bin (sudo)
```

## Authentication

Credentials are resolved per field through a layered chain (first match wins):

1. Flags `--url`, `--username`, `--password`
2. Environment `KVM_URL` / `KVM_USERNAME` / `KVM_PASSWORD`
3. Environment aliases `GLKVM_URL` / `GLKVM_USERNAME` / `GLKVM_PASSWORD`
4. gopass `env/GLKVM_URL` / `env/GLKVM_USERNAME` / `env/GLKVM_PASSWORD`
5. Config file `~/.config/kvm-cli/config.json` (mode `0600`)
6. Interactive prompt (TTY only)

```bash
# A) gopass (recommended where available)
export KVM_URL="$(gopass show -o env/GLKVM_URL)"
export KVM_USERNAME="$(gopass show -o env/GLKVM_USERNAME)"
export KVM_PASSWORD="$(gopass show -o env/GLKVM_PASSWORD)"

# B) plain env vars
export KVM_URL=https://glkvm.local
export KVM_USERNAME=admin
export KVM_PASSWORD='secret'

# C) persist to the config file
kvm-cli config set url https://glkvm.local
kvm-cli config set username admin
kvm-cli config set password 'secret'
```

Verify:

```bash
kvm-cli auth status      # resolved URL/username + session check (never prints the password)
kvm-cli auth check       # exits non-zero if the session is rejected
```

> The device ships a self-signed certificate with an invalid (1970–1979)
> validity window, so `kvm-cli` attempts verification and transparently falls
> back to insecure TLS with a warning. Pass `--insecure` to skip the first
> attempt, or set `KVM_TLS_STRICT=1` to require a valid certificate.

See [docs/config.md](docs/config.md) for the full config schema, every
environment variable, and defaults.

## Usage

```bash
# Global help and version
kvm-cli --help
kvm-cli --version

# Device information (JSON)
kvm-cli info --json
kvm-cli info --json --jq .system.kvmd.version

# Grab a screenshot
kvm-cli screenshot                             # -> timestamped file in the scratch dir
kvm-cli screenshot -o /tmp/screen.jpg
kvm-cli screenshot -o - > /tmp/screen.jpg

# Type and send keys
kvm-cli hid type "hello world" --enter
kvm-cli hid key Enter
kvm-cli hid combo ctrl+alt+del

# Mouse
kvm-cli hid mouse move 1000 500
kvm-cli hid mouse move --pct 50 50
kvm-cli hid mouse click left
kvm-cli hid mouse wheel 0 -3

# Power (see the ATX caveat below)
kvm-cli atx status
kvm-cli atx power --long --yes

# Virtual media
kvm-cli msd status
kvm-cli msd connect /dev/disk/by-uuid/0717-B213
kvm-cli msd write ./ubuntu.iso --yes

# Wake-on-LAN
kvm-cli wol scan
kvm-cli wol wake AA:BB:CC:DD:EE:FF

# Self-documentation
kvm-cli docs | less
kvm-cli skill add
kvm-cli completion zsh
```

### Agent computer-use loop

```bash
# SEE
kvm-cli screenshot -o /tmp/screen.jpg
# THINK — analyze /tmp/screen.jpg to choose a target
# ACT
kvm-cli hid mouse move 1280 720
kvm-cli hid mouse click left
kvm-cli hid type "user@example.com" --enter
# SEE again
kvm-cli screenshot -o /tmp/screen.jpg
```

### Computer-use assistance (CUA / OmniParser)

`kvm-cli cua` closes the see → think → act loop inside the CLI by pairing the
screenshot with a hosted **models platform** (`https://models.example.com` by
default). A **grounding** model (OmniParser) parses the frame into a numbered
Set-of-Mark element list. An optional **planner** chat model can pick the
element matching a natural-language instruction, but the default flow is
planner-free: the calling agent reads the element list and clicks via a selector
(`cua click --index/--text/--id`) — no chat model required.

```bash
# SEE — capture to a temp file (scratch dir, never the CWD)
kvm-cli screenshot

# GROUND — number the UI elements (captures a screenshot if no image is given)
kvm-cli cua ground

# PLAN + resolve a click by instruction. Prints the target by default;
# --execute --yes actually moves the mouse and clicks over HID.
kvm-cli cua click "click the Settings icon"
kvm-cli cua click "click Login" --execute --yes

# Inspect the platform and effective settings
kvm-cli cua models
kvm-cli cua probe          # alias: kvm-cli cua status
```

#### Deterministic targeting, waiting and region capture (no wrapper scripts)

When the label is already known, select elements natively instead of filtering a
ground JSON with Python/jq, cropping with an image tool, or sleeping in a loop:

```bash
# Select by OCR text (planner-free): best match, or every match
kvm-cli cua find --text "Sign in"
kvm-cli cua find --text "Okta" --region 700,560,1250,760 --all

# Regex / interactivity / nearest / known id; read the screen as text
kvm-cli cua find --regex "ok|cancel" --interactive --all
kvm-cli cua find --id 135
kvm-cli cua text --plaintext

# Click deterministically; ground once and act later
kvm-cli cua click --text "Next" --execute --yes
kvm-cli cua ground --json > /tmp/g.json && kvm-cli cua click --from /tmp/g.json --id 135

# Replace sleep loops: settle the UI, or wait for text to appear/vanish
kvm-cli screenshot --until-stable
kvm-cli cua wait --text "Push notification sent"
kvm-cli cua wait --text "Loading" --gone --max-wait 60s

# Read a small region at 2x without an external cropper
kvm-cli screenshot --region 1400,150,1820,360 --scale 2 -o /tmp/btn.jpg --json
```

`cua ground --json` emits `{image_path, model, width, height, count, elements[],
elapsed_ms}` where each element is `{type, interactivity, content, bbox,
bbox_norm, center}`; the global `--jq`/`--fields` work on it directly, e.g.
`kvm-cli cua ground --json --jq '.elements[].content'`.

Why the design matters:

- **Screenshots go to temp space.** `screenshot` and `streamer snapshot` write a
  unique, timestamped file under the scratch directory by default — the OS temp
  dir unless `--scratch-dir`, `KVM_SCRATCH_DIR`, or config `scratch_dir` says
  otherwise. A bare capture never writes into the current working directory.
- **The planner sees text, not pixels.** The grounded element list — never image
  bytes — is what reaches the planner. Prompts stay small and the agent's context
  is never filled with base64 JPEGs.
- **`--annotate` is opt-in.** The Set-of-Mark PNG is written only when requested
  (`--annotate <path>`, or `--annotate auto` for a scratch path), and the base64
  image is never included in JSON output.

CUA settings resolve **flag > environment > config > default**:

| Setting | Flag | Environment | Config key | Default |
|---------|------|-------------|-----------|---------|
| Platform root | `--models-url` | `KVM_MODELS_URL` | `models_url` | `https://models.example.com` |
| Grounding model | `--model` | `KVM_GROUNDING_MODEL` | `grounding_model` | auto (running grounding model) |
| Planner model | `--planner` | `KVM_PLANNER_MODEL` | `planner_model` | *(none — optional)*; `auto` picks a running chat model |
| Scratch dir | `--scratch-dir` | `KVM_SCRATCH_DIR` | `scratch_dir` | OS temp dir |

```bash
kvm-cli config set models_url https://models.example.com
kvm-cli config set planner_model hemmingway
kvm-cli config set scratch_dir /tmp/kvmshot
```

`cua click` is a write: `--execute` requires `--yes` (or `-f`/`--force`) and is
previewable with the global `--dry-run`. `find`, `text` and `wait` are read-only
and never invoke the planner. See the
[CUA section of the skill](skill/SKILL.md#computer-use-assistance-cua--omniparser)
and the [full command reference](skill/reference/commands.md#cua-computer-use-assistance)
for every flag.

### Remote transports: KVM, VNC, and RustDesk

kvm-cli drives a machine over three transports. The default is the GL.iNet KVM
(HID WebSocket + JPEG snapshot).

**VNC** is supported natively (no external tools): None, VNC DES, and Apple/ARD
(macOS Screen Sharing) authentication; RAW/CopyRect/Hextile encodings. This is
the fix for macOS 15, where `screencapture` over SSH returns blank frames.

```bash
# Standalone VNC control
kvm-cli vnc --host HOST:PORT --password "$PW" info
kvm-cli vnc --host HOST:PORT --password "$PW" screenshot -o /tmp/v.png
kvm-cli vnc --host HOST:PORT --password "$PW" mouse click left --at 490,358
kvm-cli vnc --host HOST:PORT --password "$PW" key ctrl+a
kvm-cli vnc --host mac-remote.local --username "$USER" --password "$PW" screenshot   # Apple ARD

# Run the entire computer-use pipeline over VNC
kvm-cli screenshot --vnc HOST:PORT --vnc-password "$PW" -o /tmp/v.png
kvm-cli cua find  --vnc HOST:PORT --vnc-password "$PW" --text "Sign in"
kvm-cli cua click --vnc HOST:PORT --vnc-password "$PW" --text "Next" --execute --yes
```

**RustDesk** has no headless screenshot/input API (proprietary rendezvous +
NaCl-authenticated video stream, no client library), so kvm-cli manages its
config/peers and launches connections; the session is driven through a display
transport:

```bash
kvm-cli rustdesk info            # id, relay server, password status
kvm-cli rustdesk id              # plaintext id (via the client)
kvm-cli rustdesk peers           # address book
kvm-cli rustdesk connect ID --yes
# To drive a RustDesk session on Linux, run it on :1 and use `kvm-cli vnc --host HOST:5901`.
```

### ATX caveat

ATX power control is disabled on some units. On `glkvm` the device reports
`"enabled": false`. ATX actions require confirmation with `--yes` (or
`-f`/`--force`); the same flag overrides the disabled guard, so only use it if
the ATX header is physically wired. Prefer Wake-on-LAN (`kvm-cli wol wake <mac>`)
when ATX is unavailable.

### Destructive commands

Commands that delete, overwrite, reboot, log out, or reflash require `--yes`.
Without it they print a dry run and change nothing. This covers `msd write`
/`write-remote`/`format`/`remove`, `wol remove`, `screen set-background`
/`delete-background`, `upgrade upload`/`start`/`reboot`/`reset`, `2fa
create`/`init`/`delete`, `fingerbot upgrade`, the `tailscale`/`netbird`/`zerotier`
stop/logout commands, `repeater disconnect`/`remove-saved`, `init
run`/`change-password`, the `system set-*` writes, and the `atx power`/`reset`/`click`
button presses.

## Global flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--url` | | | Device base URL (env `KVM_URL`, `GLKVM_URL`) |
| `--username` | | | Username (env `KVM_USERNAME`, `GLKVM_USERNAME`) |
| `--password` | | | Password (env `KVM_PASSWORD`, `GLKVM_PASSWORD`) |
| `--format` | | `table` | Output format: `table`\|`json`\|`plaintext`\|`yaml` |
| `--json` | `-j` | `false` | JSON output (shorthand) |
| `--plaintext` | `-p` | `false` | Tab-separated output (shorthand) |
| `--fields` | | | Comma-separated fields to include in JSON output |
| `--jq` | | | jq expression to filter JSON output |
| `--no-color` | | `false` | Disable colour (also honours `NO_COLOR`) |
| `--debug` | | `false` | Verbose debug logging to stderr |
| `--config` | | `~/.config/kvm-cli/config.json` | Config file path |
| `--timeout` | | `30s` | HTTP request timeout |
| `--insecure` | | `false` | Skip TLS certificate verification |
| `--scratch-dir` | | (OS temp dir) | Directory for transient screenshots/artifacts (env `KVM_SCRATCH_DIR`) |
| `--dry-run` | | `false` | Simulate write/destructive commands without executing them |
| `--quiet` | `-q` | `false` | Suppress non-error output on stderr |
| `--silent` | | `false` | Synonym for `--quiet` |
| `--verbose` | `-v` | `false` | Print more progress information to stderr |
| `--version` | `-V` | | Print the version |
| `--help` | `-h` | | Help for any command |

Exit codes: `0` success, `1` user/runtime error, `2` usage error.

## Documentation

- [docs/config.md](docs/config.md) — config file schema, env vars, credential chain, defaults
- [skill/SKILL.md](skill/SKILL.md) — Claude Code / agent skill (install with `kvm-cli skill add`)
- [skill/reference/commands.md](skill/reference/commands.md) — complete command reference
- [llms.txt](llms.txt) — agent-readable documentation index
- Man pages: `kvm-cli man`, or `make man`

## Configuration

Settings live in `~/.config/kvm-cli/config.json` (mode `0600`):

```bash
kvm-cli config list
kvm-cli config get url
kvm-cli config set url https://glkvm.local
kvm-cli config set timeout 45s
kvm-cli config set output_format json
kvm-cli config set models_url https://models.example.com
kvm-cli config set grounding_model omniparser
kvm-cli config set planner_model hemmingway
kvm-cli config set scratch_dir /tmp/kvmshot
kvm-cli config unset password
```

Supported keys: `url`, `username`, `password`, `timeout`, `insecure`,
`output_format` (alias `format`), plus the CUA keys `models_url`,
`grounding_model`, `planner_model`, and `scratch_dir`. Full schema:
[docs/config.md](docs/config.md).

## Claude Code skill

```bash
kvm-cli skill print   # print SKILL.md
kvm-cli skill path    # install directory
kvm-cli skill add     # install to ~/.claude/skills/kvm-cli/
```

## Development

```bash
make deps       # download + tidy modules
make build      # build ./kvm-cli
make test       # smoke tests (no device required)
make test-unit  # unit tests with -race + coverage
make fmt        # go fmt
make lint       # golangci-lint
make man        # generate man pages
make check      # fmt + lint + test + test-unit
```

## License

MIT
