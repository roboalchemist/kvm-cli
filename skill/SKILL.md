---
name: kvm-cli
description: Control a remote computer through the GL.iNet Comet PoE Remote KVM (GL-RM1PE) — capture screenshots, inject keyboard/mouse HID input, manage virtual media, and control ATX power. Use when driving a machine over a PiKVM-compatible remote KVM for computer-use automation.
scope: both
allowed-tools: Bash(kvm-cli:*)
---

# kvm-cli

Agent-first CLI for the **GL.iNet Comet PoE Remote KVM (GL-RM1PE)** — a
PiKVM-based device that exposes a REST API, a WebSocket HID channel, and a JPEG
snapshot endpoint. It lets an agent see and drive a remote machine's screen and
keyboard/mouse, just like a physical operator sitting in front of it.

The core computer-use surface:

| Need | Command |
|------|---------|
| See the screen | `kvm-cli screenshot -o /tmp/shot.jpg` |
| See the screen over VNC | `kvm-cli vnc --host HOST:PORT --password "$PW" screenshot` |
| Drive any transport with CUA | `kvm-cli cua find --vnc HOST:PORT --vnc-password "$PW" --text "Sign in"` |
| RustDesk id / peers | `kvm-cli rustdesk id` · `kvm-cli rustdesk peers` |
| See part of the screen, zoomed | `kvm-cli screenshot --region 1400,150,1820,360 --scale 2 -o /tmp/btn.jpg` |
| Wait for the UI to settle | `kvm-cli screenshot --until-stable` |
| Find an element by text | `kvm-cli cua find --text "Sign in"` |
| Click an element by text | `kvm-cli cua click --text "Next" --execute --yes` |
| Dump the screen's OCR text | `kvm-cli cua text --plaintext` |
| Wait for text to appear | `kvm-cli cua wait --text "Push notification sent"` |
| Type text | `kvm-cli hid type "hello"` |
| Press a key / combo | `kvm-cli hid key Enter` · `kvm-cli hid combo ctrl+alt+del` |
| Move / click the mouse | `kvm-cli hid mouse move 1000 500` · `kvm-cli hid mouse click left` · `kvm-cli hid mouse click left --at 961,803` |
| Ground + click by instruction | `kvm-cli cua click "click the Settings icon"` |
| Power the target | `kvm-cli atx power` (see [ATX caveat](#atx-caveat)) |

## When to use

Use `kvm-cli` when you need to:

- Capture a screenshot of a remote machine's display for an agent vision loop
- Inject keyboard text, keys, or combos into the target
- Move, click, or scroll the target's mouse
- Mount an ISO/image as virtual media (`msd`) or boot via Wake-on-LAN (`wol`)
- Control target power (`atx`), or inspect/modify the KVM device itself
  (`system`, `info`, `auth`, network overlays, firmware)

## Authentication

Credentials resolve through a layered chain (first match wins per field):

1. Flags `--url`, `--username`, `--password`
2. Environment `KVM_URL` / `KVM_USERNAME` / `KVM_PASSWORD`
3. Environment aliases `GLKVM_URL` / `GLKVM_USERNAME` / `GLKVM_PASSWORD`
4. gopass `env/GLKVM_URL` / `env/GLKVM_USERNAME` / `env/GLKVM_PASSWORD`
5. Config file `~/.config/kvm-cli/config.json`
6. Interactive prompt (TTY only)

### Option A — gopass (recommended on machines with gopass)

```bash
export KVM_URL="$(gopass show -o env/GLKVM_URL)"
export KVM_USERNAME="$(gopass show -o env/GLKVM_USERNAME)"
export KVM_PASSWORD="$(gopass show -o env/GLKVM_PASSWORD)"
```

### Option B — plain environment variables

```bash
export KVM_URL=https://glkvm.local
export KVM_USERNAME=admin
export KVM_PASSWORD='secret'
```

### Option C — persist to the config file

```bash
kvm-cli config set url https://glkvm.local
kvm-cli config set username admin
kvm-cli config set password 'secret'
```

Verify connectivity:

```bash
kvm-cli auth status
kvm-cli auth check
```

`auth status` prints the resolved URL/username and whether a session can be
established; the password is never printed.

### TLS

The GL-RM1PE ships a self-signed certificate with an invalid (1970–1979)
validity window, so certificate verification always fails. `kvm-cli` tries
verification first and, only on a certificate error, prints a one-line warning
and retries with insecure TLS. Pass `--insecure` to skip the first attempt, or
set `KVM_TLS_STRICT=1` to require a valid certificate.

### Output & config reference

- Data goes to **stdout**; warnings/progress/errors go to **stderr**.
- `--json` (`-j`) emits machine-readable JSON; `--plaintext` (`-p`) emits
  tab-separated values; `--format table|json|plaintext|yaml` selects a mode.
- `--fields a,b` projects JSON keys; `--jq '<expr>'` filters JSON.
- Full schema, env vars, and defaults: [docs/config.md](../../docs/config.md).

## Quick start

```bash
# Discover the command tree
kvm-cli --help

# Confirm auth
kvm-cli auth status

# Grab a frame
kvm-cli screenshot -o /tmp/screen.jpg

# Type into the target and submit
kvm-cli hid type "echo hello" --enter

# Move and click
kvm-cli hid mouse move 960 540
kvm-cli hid mouse click left

# Device info (JSON)
kvm-cli info --json --jq .system.kvmd.version
```

## Agent computer-use loop

A typical see → think → act cycle. Screenshot to a file, analyze it (with a
vision model or your own reasoning), then act.

```bash
#!/usr/bin/env bash
set -euo pipefail

# 1. SEE — capture the current screen
kvm-cli screenshot -o /tmp/screen.jpg
#    (the device returns a JPEG; the attached target renders 2560x1440)

# 2. THINK (outside the CLI) — analyze /tmp/screen.jpg to decide the next action.
#    Mouse coordinates are in the framebuffer's pixel space (e.g. 2560x1440).

# 3. ACT — move the pointer to a UI element and click it
kvm-cli hid mouse move 1280 720
kvm-cli hid mouse click left

# 4. ACT — type into a focused field and submit
kvm-cli hid type "user@example.com" --enter

# 5. ACT — send a key combo (e.g. open a terminal)
kvm-cli hid combo ctrl+alt+t

# 6. SEE again — loop
kvm-cli screenshot -o /tmp/screen.jpg
```

Useful variants:

```bash
# Percentage-based pointer moves (resolution independent)
kvm-cli hid mouse move --pct 50 50

# Relative nudges and scrolling
kvm-cli hid mouse move --relative 5 -5
kvm-cli hid mouse wheel 0 -3

# Click-and-drag
kvm-cli hid mouse down left
kvm-cli hid mouse move 1400 900
kvm-cli hid mouse up left

# Capture several consecutive frames
kvm-cli screenshot --frames 5 -o /tmp/frame.jpg

# Write the JPEG to stdout instead of a file
kvm-cli screenshot -o - > /tmp/screen.jpg
```

Always re-capture after acting: HID events are asynchronous and the screen may
need a moment to update.

## Computer-use assistance (CUA / OmniParser)

`kvm-cli cua` closes the see → think → act loop inside the CLI by pairing the
screenshot with a hosted **models platform** (default
`https://models.example.com`). A **grounding** model (OmniParser) turns a frame
into a numbered Set-of-Mark element list; a **planner** chat model picks the
element that matches a natural-language instruction; the chosen element's center
becomes a click.

```bash
# 1. SEE — capture to a temp file (scratch dir, never the CWD)
kvm-cli screenshot

# 2. GROUND — number the elements (auto-captures a screenshot if no image is given)
kvm-cli cua ground

# 3. PLAN — resolve an instruction to a click. Prints the target by default;
#    --execute --yes actually moves the mouse and clicks over the HID WebSocket.
kvm-cli cua click "click the Settings icon"
kvm-cli cua click "click Login" --execute --yes
```

Inspect the platform with `kvm-cli cua models`, and the effective settings +
grounding readiness with `kvm-cli cua probe` (alias `kvm-cli cua status`). Use
`kvm-cli cua parse` for the Microsoft `omniparserserver` `/parse/`
compatibility endpoint.

### Deterministic targeting (no planner, no scripts)

When you already know the label you want, do **not** route through the planner
model and do **not** shell out to Python/jq to filter a ground JSON. The CLI
selects the element itself:

```bash
# Ground + select by OCR text (case-insensitive substring), planner-free:
kvm-cli cua find --text "Sign in"

# Narrow by region and print every match:
kvm-cli cua find --text "Okta" --region 700,560,1250,760 --all

# Regex, interactivity, nearest-to-a-point, or a known element id:
kvm-cli cua find --regex "ok|cancel" --all
kvm-cli cua find --interactive --all
kvm-cli cua find --text "Next" --nearest 961,681
kvm-cli cua find --id 135

# Read the screen as text (id, type, OCR content, center) — no planner, no image:
kvm-cli cua text --plaintext

# Click deterministically (add --execute --yes to actually click):
kvm-cli cua click --text "Next"
kvm-cli cua click --text "Sign in" --region 600,250,1250,760 --execute --yes

# Ground once, act later (ground and act are separable):
kvm-cli cua ground --json > /tmp/g.json
kvm-cli cua click --from /tmp/g.json --id 135 --execute --yes
```

**Selector flags** (shared by `find`, `click`, `wait`): `--text S` (substring),
`--exact` (full match), `--regex RE` (case-insensitive RE2), `--interactive`,
`--region X1,Y1,X2,Y2` (center inside), `--nearest X,Y` (sort by distance),
`--index N` / `--id N` (pick one), `--from FILE` (reuse a saved ground).
Multiple criteria AND together; `find` prints the best match unless `--all`.

### Waiting / settling (replace `sleep` loops)

```bash
# Capture until two consecutive frames are identical (UI settled):
kvm-cli screenshot --until-stable

# Poll screenshot+ground until text appears (or, with --gone, disappears):
kvm-cli cua wait --text "Push notification sent"
kvm-cli cua wait --text "Loading" --gone --max-wait 60s
kvm-cli cua wait --regex "signed in|dashboard"
```

`cua wait` exits 0 on success and returns a `TIMEOUT` coded error on the
deadline. `--max-wait` (default 30s, avoids clashing with the global HTTP
`--timeout`) and `--interval` (default 1s) tune the loop.

### Region capture & zoom

`screenshot --region X1,Y1,X2,Y2 --scale N` crops and upscales a frame so a small
button is readable without an external image tool. JSON/YAML output carries
`region` and `scale`; map a crop coordinate back to source pixels with
`source_x = region.x1 + crop_x / scale`. You can also feed a crop straight back
into grounding and map its own coordinates with `--map`/`--map-scale`:

```bash
kvm-cli screenshot --region 700,560,1250,760 --scale 2 -o /tmp/crop.jpg --json
kvm-cli cua find --image /tmp/crop.jpg --map 700,560,1250,760 --map-scale 2 --text Next
```

### `cua ground --json` schema

```jsonc
{
  "image_path": "/tmp/kvm-cua-*.jpg",   // the grounded file (temp space)
  "model": "omniparser",
  "width": 2560, "height": 1440,        // source frame pixels
  "count": 2,
  "elements": [
    {
      "type": "text",                   // OmniParser label (text|icon|...)
      "interactivity": true,
      "content": "Sign in",             // OCR text (may be empty for icons)
      "bbox": [x1, y1, x2, y2],         // pixel xyxy
      "bbox_norm": [nx1, ny1, nx2, ny2],// 0..1 xyxy
      "center": [cx, cy]                // pixel center — use for clicks
    }
  ],
  "elapsed_ms": 812.4,
  "annotated_path": "/tmp/kvm-som-*.png" // only with --annotate
}
```

With the global `--jq` you rarely need a script:

```bash
# All element texts:
kvm-cli cua ground --json --jq '.elements[].content'
# Click centers of interactive elements:
kvm-cli cua ground --json --jq '[.elements[] | select(.interactivity) | .center]'
# Just the count:
kvm-cli cua ground --json --jq '.count'
# Project fields without jq:
kvm-cli cua ground --json --fields image_path,count
```

### Why screenshots and image bytes stay out of the model context

- **Screenshots go to temp space.** `screenshot` (and `streamer snapshot`) write
  a unique, timestamped file under the *scratch directory* by default — the OS
  temp dir unless `--scratch-dir`, `KVM_SCRATCH_DIR`, or config `scratch_dir`
  overrides it. A bare capture never drops a file in the current working
  directory, so an agent loop does not litter the repo.
- **The planner receives the element list, never the image.** The grounding model
  returns numbered elements; only that text list (id, type, OCR content, center)
  is placed in the planner prompt. **Image bytes are never put into the model
  context**, so prompts stay small.
- **`--annotate` is opt-in.** The Set-of-Mark PNG is written only when requested
  (`--annotate <path>`, or `--annotate auto` for a scratch path), and the base64
  screenshot is never included in JSON output.

### Configuration

`cua` settings resolve **flag > environment > config > default**:

| Setting | Flag | Environment | Config key | Default |
|---------|------|-------------|-----------|---------|
| Platform root | `--models-url` | `KVM_MODELS_URL` | `models_url` | `https://models.example.com` |
| Grounding model | `--model` | `KVM_GROUNDING_MODEL` | `grounding_model` | auto (a running grounding model) |
| Planner model | `--planner` | `KVM_PLANNER_MODEL` | `planner_model` | auto (a running chat model) |
| Scratch dir | `--scratch-dir` | `KVM_SCRATCH_DIR` | `scratch_dir` | OS temp dir |

```bash
kvm-cli config set models_url https://models.example.com
kvm-cli config set planner_model hemmingway
kvm-cli config set scratch_dir /tmp/kvmshot
```

`cua click` is a write: `--execute` requires `--yes` (or `-f`/`--force`) and is
previewable with the global `--dry-run`. The `find`, `text` and `wait` commands
are read-only and never invoke the planner; `click` reaches the planner only in
instruction mode (no selector). Full flags and examples:
[reference/commands.md](reference/commands.md#cua-computer-use-assistance).

## Remote transports: KVM, VNC, and RustDesk

kvm-cli can drive a machine over three transports. The default is the GL.iNet
KVM (HID WebSocket + JPEG snapshot). **VNC** is fully supported natively, and is
the right choice when a machine exposes a VNC server (macOS Screen Sharing,
TigerVNC, etc.) — including macOS 15, where `screencapture` over SSH returns
blank frames.

### VNC

```bash
# Standalone VNC control (flag > env: KVM_VNC_HOST/VNC_HOST, ..._USERNAME, ..._PASSWORD)
kvm-cli vnc --host HOST:PORT --password "$PW" info
kvm-cli vnc --host HOST:PORT --password "$PW" screenshot -o /tmp/v.png
kvm-cli vnc --host HOST:PORT --password "$PW" mouse click left --at 490,358
kvm-cli vnc --host HOST:PORT --password "$PW" key enter          # or ctrl+a
kvm-cli vnc --host HOST:PORT --password "$PW" type "hello" --enter

# Run the whole computer-use pipeline over VNC:
kvm-cli screenshot --vnc HOST:PORT --vnc-password "$PW" -o /tmp/v.png
kvm-cli cua text   --vnc HOST:PORT --vnc-password "$PW"
kvm-cli cua find   --vnc HOST:PORT --vnc-password "$PW" --text "Sign in"
kvm-cli cua click  --vnc HOST:PORT --vnc-password "$PW" --text "Next" --execute --yes

# macOS Screen Sharing uses Apple (ARD) auth: pass the account username.
kvm-cli vnc --host mac-remote.local --username "$USER" --password "$PW" screenshot
```

Supported authentication: None, VNC DES, and Apple/ARD DH (macOS). Encodings:
RAW, CopyRect, Hextile, plus the DesktopSize pseudo-encoding. No external tools
(`vncdo`, `xtightvncviewer`) are needed. `--vnc` on `screenshot`/`cua` also
supports `--region`/`--scale` (the crop is PNG for VNC, JPEG for KVM).

### RustDesk

RustDesk has **no headless screenshot/input API** — its protocol is a proprietary
rendezvous + NaCl-authenticated video stream with no client library — so kvm-cli
does not re-implement it. It manages RustDesk config/peers and launches
connections; the resulting session is driven through a display transport:

```bash
kvm-cli rustdesk info                 # id, relay server, password status
kvm-cli rustdesk id                   # plaintext id (via the client)
kvm-cli rustdesk peers                # address book
kvm-cli rustdesk status               # is the local rustdesk process running
kvm-cli rustdesk connect ID --yes     # launch rustdesk --connect ID
```

To drive a RustDesk session on a Linux host, run it on an X display (e.g. `:1`)
and point kvm-cli's VNC transport at that display:
`kvm-cli vnc --host HOST:5901 --password "$PW" screenshot`. Passwords are never
printed. See [reference/commands.md](reference/commands.md#vnc) and
[#rustdesk](reference/commands.md#rustdesk).

## HID notes

- **Key names are DOM `KeyboardEvent.code`**, not characters: `KeyA`, `Digit1`,
  `Enter`, `Escape`, `ControlLeft`, `MetaLeft`, `CapsLock`, `F4`, ...
  Run `kvm-cli hid keys` to list every accepted value.
- **`hid key <name>`** taps by default (press + release). Use `--down` to hold,
  `--up` to release.
- **`hid combo a+b+c`** presses in order, then releases in reverse.
- **`hid type <text>`** uses the device's layout-aware print endpoint
  (`/api/hid/print`) — this handles arbitrary strings, spaces, and shifted
  characters without you enumerating keys. Set `--keymap de` for non-US layouts,
  `--enter` to submit, or `--file ./msg.txt` to type a file's contents.
- **`hid print <text>`** is the same endpoint without WebSocket key events.
- **Mouse coordinates** in absolute mode are framebuffer pixels (origin
  top-left). `--pct` treats them as `0..100` percentages; `--relative` treats
  them as deltas. Internally absolute moves are scaled to the device's signed
  16-bit range.
- **`hid mouse click <btn> --at X,Y`** moves then clicks in one round trip
  (`--at-pct` for percentages) — use it instead of `hid mouse move` followed by
  `hid mouse click`.
- **Mouse output mode** — inspect with `hid status` (`.mouse.outputs`) and change
  with `hid set-mouse-output usb|usb_rel|usb_hybrid|usb_touch`.
- Screenshots require an active stream: `kvm-cli screenshot` opens one
  internally, so no separate step is needed.

## ATX caveat

ATX power control is **disabled on some units**. On `glkvm` the device
reports `enabled=false`:

```bash
kvm-cli atx status --json
# { "enabled": false, "busy": false, "power": "off", "leds": {...} }
```

`kvm-cli atx power` / `atx reset` require confirmation with `--yes` (or its
synonym `-f`/`--force`), exactly like other writes. On a unit that reports
`enabled=false`, the same confirmation also overrides the disabled guard — only
use it if the ATX header is physically wired and you intend to override. Prefer
Wake-on-LAN (`kvm-cli wol wake <mac>`) for booting when ATX is unavailable.

```bash
kvm-cli atx power --yes            # press power
kvm-cli atx power --long --yes     # press and hold
kvm-cli atx reset --yes            # press reset
kvm-cli atx click power_long --yes # explicit button
kvm-cli atx power --dry-run        # preview only, exits 0
```

## Safety: destructive operations

Commands that delete, overwrite, reboot, log out, or reflash **require `--yes`**.
Without it they print a dry run and make no changes. This list includes:

- `msd write`, `msd write-remote`, `msd format`, `msd remove`
- `wol remove`
- `screen set-background`, `screen delete-background`
- `upgrade upload`, `upgrade start`, `upgrade reboot`, `upgrade reset`
- `2fa create`, `2fa init`, `2fa delete`
- `fingerbot upgrade`
- `netbird stop`, `netbird logout`
- `zerotier stop`
- `tailscale stop`, `tailscale logout`
- `repeater disconnect`, `repeater remove-saved`
- `init run`, `init change-password`
- `atx power`, `atx reset`, `atx click`
- `system set-*` (hostname, time, timezone, param, network, config, firewall,
  ssl-cert)

Review the dry-run output before adding `--yes`.

## Command groups

| Group | Purpose |
|-------|---------|
| `auth` | Log in, check session, status, logout |
| `info` | Device information document |
| `system` | Read information; write network/config/param/ssl-cert |
| `screenshot` | Capture a JPEG frame (default output: scratch dir) |
| `streamer` | Video streamer status, snapshots, encoder params |
| `hid` | Keyboard/mouse injection (`key`, `combo`, `type`, `print`, `mouse`) |
| `cua` | Computer-use assistance: ground, plan, and click via the models platform |
| `atx` | Target power/reset buttons |
| `msd` | Virtual media (mount ISO/images, upload, format) |
| `wol` | Wake-on-LAN list/scan/add/wake/remove |
| `switch` | USB switch ports (unsupported on this firmware) |
| `screen` | Custom screen background/mode (unsupported on this firmware) |
| `tailscale` / `netbird` / `zerotier` | Overlay-network clients |
| `repeater` / `ap` | Wi-Fi station and access-point modes |
| `modem` | Cellular modem (AT commands, SIM settings) |
| `upgrade` | Firmware version, check, download, install, reboot |
| `twofa` (`2fa`) | TOTP two-factor authentication |
| `asr` | Automatic speech recognition (unsupported on this device) |
| `fingerbot` | Fingerbot actuator (requires attached hardware) |
| `init` | First-run state and admin password |
| `turn` | TURN/WebRTC relay configuration |
| `config` | Persisted configuration (`get`/`set`/`list`/`unset`) |
| `skill` / `docs` / `man` / `completion` / `version` | Self-documentation |

## Exit codes

`0` success · `1` user/correctable or runtime error · `2` usage error.
In `--json` mode errors are structured on stderr:
`{"error": {"code": "...", "message": "..."}}`.

## Full command reference

See [reference/commands.md](reference/commands.md) for every command,
subcommand, flag, default, and example.

## Self-documentation

```bash
kvm-cli docs              # print the README
kvm-cli skill print       # print this SKILL.md
kvm-cli man               # print the man page
kvm-cli completion zsh    # shell completions
```
