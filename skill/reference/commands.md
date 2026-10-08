# kvm-cli command reference

Complete reference for every `kvm-cli` command, organised by resource. Every
command, flag, default, and example below matches the binary's `--help` output.

- [Global flags](#global-flags)
- [Built-ins](#built-ins-docs-version-man-completion-skill-config)
- [auth](#auth) · [info](#info) · [system](#system)
- [screenshot](#screenshot) · [streamer](#streamer) · [hid](#hid-keyboard--mouse) · [atx](#atx) · [cua](#cua-computer-use-assistance)
- [vnc](#vnc) · [rustdesk](#rustdesk)
- [msd](#msd) · [wol](#wol) · [switch](#switch) · [screen](#screen)
- [tailscale](#tailscale) · [netbird](#netbird) · [zerotier](#zerotier) · [repeater](#repeater) · [ap](#ap) · [modem](#modem)
- [upgrade](#upgrade) · [twofa](#twofa-2fa) · [asr](#asr) · [fingerbot](#fingerbot) · [init](#init) · [turn](#turn)

---

## Global flags

Every command accepts these persistent flags:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--url` | | | Device base URL (env `KVM_URL`, `GLKVM_URL`) |
| `--username` | | | Username (env `KVM_USERNAME`, `GLKVM_USERNAME`) |
| `--password` | | | Password (env `KVM_PASSWORD`, `GLKVM_PASSWORD`) |
| `--format` | | `table` | Output format: `table`\|`json`\|`plaintext`\|`yaml` |
| `--json` | `-j` | `false` | JSON output (shorthand for `--format json`) |
| `--plaintext` | `-p` | `false` | Tab-separated output (shorthand for `--format plaintext`) |
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
| `--version` | `-V` | | version for kvm-cli |
| `--help` | `-h` | | help for any command |

Destructive commands additionally take `--yes` (or its synonym `-f`/`--force`) to
confirm (see each command). The `atx` action commands use the same gate.

```bash
# JSON with field projection and jq filtering
kvm-cli info --json --fields system
kvm-cli info --json --jq .system.kvmd.version

# Tab-separated for piping
kvm-cli hid keys --plaintext
```

---

## Built-ins: docs, version, man, completion, skill, config

### `kvm-cli docs`
Print the embedded README to stdout.

```bash
kvm-cli docs
kvm-cli docs | less
kvm-cli docs > docs.md
```

### `kvm-cli version`
Print the kvm-cli version.

```bash
kvm-cli version
kvm-cli version --json
kvm-cli version --format yaml
kvm-cli version --json --fields version
```

### `kvm-cli man [command]`
Print a roff(7) man page for kvm-cli or one of its subcommands to stdout.

```bash
kvm-cli man              # root man page
kvm-cli man config       # man page for the config command
kvm-cli man | man -l -   # view with man
```

### `kvm-cli completion [bash|zsh|fish|powershell]`
Generate shell completion scripts.

```bash
kvm-cli completion bash
kvm-cli completion zsh > "${fpath[1]}/_kvm-cli"
kvm-cli completion fish | source
kvm-cli completion powershell | Out-String | Invoke-Expression
```

### `kvm-cli skill`
Manage the embedded Claude Code skill.

| Subcommand | Description |
|------------|-------------|
| `skill print` | Print the embedded `SKILL.md` to stdout |
| `skill path` | Print the skill install directory (`~/.claude/skills/kvm-cli`) |
| `skill add` | Install the embedded skill into `~/.claude/skills/kvm-cli/` |

```bash
kvm-cli skill print
kvm-cli skill path
kvm-cli skill add
```

### `kvm-cli config`
Read and write settings in `~/.config/kvm-cli/config.json` (mode `0600`).

| Subcommand | Description |
|------------|-------------|
| `config list` | Show all configuration values |
| `config get <key>` | Show a single value (or `(not set)`) |
| `config set <key> <value>` | Validate and persist a value |
| `config unset <key>` | Remove a value, restoring the default |

Supported keys: `url`, `username`, `password`, `timeout`, `insecure`,
`output_format` (alias `format`). See [docs/config.md](../../docs/config.md).

```bash
kvm-cli config list
kvm-cli config get url
kvm-cli config set url https://glkvm.local
kvm-cli config set username admin
kvm-cli config set timeout 45s
kvm-cli config set output_format json
kvm-cli config unset password
```

---

## auth

Manage the authenticated session with the KVM device. The device uses
form-encoded login (`user`/`passwd`) and a `token` header on subsequent
requests; kvm-cli handles this transparently.

### `kvm-cli auth login`
Authenticate against the device with the resolved credentials and report the
resulting token. The token itself is not printed.

```bash
kvm-cli auth login
kvm-cli auth login --json
```

### `kvm-cli auth check`
Log in and verify the resulting token against `/api/auth/check`. Exits non-zero
if the session is rejected.

```bash
kvm-cli auth check
kvm-cli auth check --json
```

### `kvm-cli auth status`
Show the resolved URL and username, whether a password is configured, and
whether a session can be established. The password is never printed.

```bash
kvm-cli auth status
kvm-cli auth status --json
```

Real output (table):

```
 STATUS │ URL                                  │ USERNAME │ PASSWORD │ AUTHENTICATED │ DETAIL
 ok     │ https://glkvm.local │ admin    │ set      │ yes           │ session ok
```

### `kvm-cli auth logout`
Terminate the device session (`POST /api/auth/logout`) and clear any locally
persisted session.

```bash
kvm-cli auth logout
kvm-cli auth logout --json
```

---

## info

### `kvm-cli info`
Show the remote KVM's information document: authentication state, system
groups, and the `kvmd` extras (`kvmd-ipmi`, `kvmd-janus`, ...).

```bash
kvm-cli info
kvm-cli info --json
kvm-cli info --json --fields system
kvm-cli info --json --jq .system.kvmd.version
```

---

## system

Inspect the remote KVM's system information, and write settings where the
firmware supports it. Write subcommands require `--yes`.

### Read subcommands

| Command | Description | Example |
|---------|-------------|---------|
| `system capability` | Hardware capability map: CPU model, MIPI bridge, OTG/USB versions, partition paths | `kvm-cli system capability --json` |
| `system config` | Nested config object (keyboard/mouse, video, HID, tips, shortcuts) | `kvm-cli system config --jq .keymap` |
| `system firewall` | Firewall configuration (`GET /api/system/get_firewall_config`) | `kvm-cli system firewall --json` |
| `system hostname` | Device hostname (`GET /api/system/get_hostname`) | `kvm-cli system hostname --plaintext` |
| `system network` | Active interface: address, netmask, gateway, MAC, DHCP, DNS | `kvm-cli system network --json` |
| `system otg` | USB On-The-Go gadget functions presented to the target | `kvm-cli system otg --json` |
| `system param` | Flat key/value system parameters | `kvm-cli system param --json --fields otg_product` |
| `system time` | Device time and timezone (`GET /api/system/time`) | `kvm-cli system time --json` |
| `system timezone` | Current timezone; `--list` enumerates every timezone | `kvm-cli system timezone --list` |

### `kvm-cli system timezone`
Flags: `--list` — list every available timezone instead of the current one.

```bash
kvm-cli system timezone
kvm-cli system timezone --list
kvm-cli system timezone --list --json
```

### Write subcommands (require `--yes`)

All of these support a dry run: omit `--yes` and the command prints what it
would do without contacting the device.

| Command | Flags | Example |
|---------|-------|---------|
| `system set-hostname <hostname>` | `--yes` | `kvm-cli system set-hostname glkvm --yes` |
| `system set-time [unix-seconds]` | `--set key=value`, `--yes` | `kvm-cli system set-time 1760000000 --yes` |
| `system set-timezone <timezone>` | `--set key=value`, `--yes` | `kvm-cli system set-timezone America/Los_Angeles --yes` |
| `system set-param` | `--set key=value` (repeatable), `--yes` | `kvm-cli system set-param --set privacy_enable=true --yes` |
| `system set-network` | `--mode dhcp\|static`, `--ip`, `--netmask`, `--gateway`, `--dns`, `--set key=value`, `--yes` | `kvm-cli system set-network --mode dhcp --yes` |
| `system set-config` | `--set key=value` (repeatable), `--file <path\|->`, `--yes` | `kvm-cli system set-config --set theme_mode=dark --yes` |
| `system set-firewall` | `--enable`, `--enable-v6`, `--set key=value`, `--file <path\|->`, `--yes` | `kvm-cli system set-firewall --enable --yes` |
| `system ssl-cert` | `--cert-file`, `--key-file`, `--default`, `--yes` | `kvm-cli system ssl-cert --cert-file cert.pem --key-file key.pem --yes` |

```bash
kvm-cli system set-network --mode static --ip 10.0.0.5 --netmask 255.255.255.0 --gateway 10.0.0.1 --dns 1.1.1.1,8.8.8.8 --yes
kvm-cli system set-config --file config.json --yes
cat config.json | kvm-cli system set-config --file - --yes
kvm-cli system set-network --mode dhcp          # dry run
```

---

## screenshot

### `kvm-cli screenshot`
Capture a JPEG frame from the KVM's video stream. Internally it connects a
`stream=1` WebSocket client (the device returns `503` for a snapshot otherwise)
and then fetches the frame.

By default the JPEG is written to a unique, timestamped file under the **scratch
directory** — the OS temp dir unless `--scratch-dir`, `$KVM_SCRATCH_DIR`, or the
config `scratch_dir` key says otherwise. A bare invocation therefore never writes
into the current working directory. With `--frames N` (`N > 1`) each frame is
written as `<name>-001.jpg`, `<name>-002.jpg`, ...

Flags:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--output` | `-o` | scratch dir | Output file, or `-` for stdout |
| `--frames` | | `1` | Number of consecutive frames to capture |
| `--keep-alive` | | `false` | Hold the stream socket open until interrupted |
| `--region` | | | Crop to `X1,Y1,X2,Y2` source pixels |
| `--scale` | | `1` | Nearest-neighbor upscale factor |
| `--until-stable` | | `false` | Capture until two consecutive frames are identical |
| `--stable-interval` | | `400ms` | Poll interval for `--until-stable` |
| `--stable-timeout` | | `15s` | Give up on `--until-stable` after this long |

`--region`/`--scale` crop and zoom a frame so a small button is readable without
an external image tool. In JSON/YAML output the metadata carries `source_width`,
`source_height`, `region`, and `scale`; map a crop coordinate back to source
pixels with `source_x = region.x1 + crop_x / scale`. `--until-stable` removes
`sleep`+re-capture loops by waiting for two byte-identical frames.

The scratch directory itself is selected by the global `--scratch-dir` flag
(env `KVM_SCRATCH_DIR`, config `scratch_dir`), resolved
**flag > env > config > OS temp dir**.

```bash
kvm-cli screenshot                             # -> timestamped file in the scratch dir
kvm-cli screenshot -o /tmp/screen.jpg
kvm-cli screenshot -o - > /tmp/screen.jpg
kvm-cli screenshot --scratch-dir /tmp/kvmshot  # scratch dir for this run
kvm-cli screenshot --frames 5 -o /tmp/frame.jpg
kvm-cli screenshot --region 1400,150,1820,360 --scale 2 -o /tmp/btn.jpg --json
kvm-cli screenshot --until-stable
kvm-cli screenshot --keep-alive
```

Real output: a `2560x1440` baseline JPEG (≈64 KB) from the attached target, and
`Saved <path> (<bytes> bytes, <w>x<h>)` on stderr.

---

## streamer

Inspect the KVM video streamer and capture snapshots.

### `kvm-cli streamer status`
Show the streamer state, encoder parameters, limits, and feature flags.

```bash
kvm-cli streamer status
kvm-cli streamer status --json
```

### `kvm-cli streamer snapshot`
Capture a JPEG snapshot from the video stream. This is an alias for
`kvm-cli screenshot` with the same output semantics: by default it writes a
unique, timestamped file under the **scratch directory** (never the current
working directory), and `-o -` writes raw JPEG bytes to stdout.

Flags:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--output` | `-o` | scratch dir | Output file, or `-` for stdout |
| `--frames` | | `1` | Number of consecutive frames to capture |
| `--keep-alive` | | `false` | Hold the stream socket open until interrupted |

```bash
kvm-cli streamer snapshot
kvm-cli streamer snapshot -o /tmp/screen.jpg
kvm-cli streamer snapshot -o - > /tmp/screen.jpg
kvm-cli streamer snapshot --keep-alive
```

### `kvm-cli streamer set-params`
Change streamer encoder parameters via `POST /api/streamer/set_params`. Only the
flags you pass are sent.

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--quality int` | | Encoder quality |
| `--fps int` | | Desired frames per second |
| `--h264-bitrate int` | | H.264 bitrate (kbps) |
| `--h264-gop int` | | H.264 GOP size |
| `--venc-mode string` | | Video encoder mode |
| `--video-format int` | | Video format selector |
| `--zero-delay` | | Enable zero-delay encoding |

```bash
kvm-cli streamer set-params --quality 90
kvm-cli streamer set-params --fps 30 --h264-bitrate 4000
kvm-cli streamer set-params --zero-delay=false
```

---

## hid (keyboard & mouse)

Control the target's keyboard and mouse through the device's HID WebSocket
(`/api/ws?auth_token=...`). Key names are **DOM `KeyboardEvent.code`** values
(`KeyA`, `Enter`, `ControlLeft`, `CapsLock`, ...). Run `hid keys` for the full
list.

### `kvm-cli hid status`
Show whether HID is enabled and online, plus keyboard LED state and mouse
output mode.

```bash
kvm-cli hid status
kvm-cli hid status --json
kvm-cli hid status --json --fields enabled,online,connected
```

### `kvm-cli hid keys`
List every exact DOM `KeyboardEvent.code` accepted by `hid key` and `hid combo`.

```bash
kvm-cli hid keys
kvm-cli hid keys --plaintext
```

### `kvm-cli hid key <name>`
Inject a keyboard event. By default the key is tapped (pressed and released).

Flags: `--down` (hold), `--up` (release), `--tap` (default).

```bash
kvm-cli hid key CapsLock
kvm-cli hid key Enter
kvm-cli hid key KeyA --down
kvm-cli hid key KeyA --up
kvm-cli hid key ctrl --down && kvm-cli hid key c --tap
```

### `kvm-cli hid combo <key1+key2+...>`
Send a key combination as a sequence of HID events: keys are pressed in order,
then released in reverse.

```bash
kvm-cli hid combo ctrl+alt+del
kvm-cli hid combo ctrl+shift+t
kvm-cli hid combo MetaLeft+KeyL
kvm-cli hid combo alt+F4
```

### `kvm-cli hid type <text>`
Type text on the target using the device's layout-aware print endpoint.

Flags: `--enter` (append Enter), `--file <path>` (read text from a file),
`--keymap <layout>` (default `en-us`).

```bash
kvm-cli hid type "hello world"
kvm-cli hid type "password123" --enter
kvm-cli hid type --file ./message.txt
kvm-cli hid type "user@example.com" --keymap de
```

### `kvm-cli hid print <text>`
Type text using `POST /api/hid/print` directly (no WebSocket key events).
Flag: `--keymap <layout>` (default `en-us`).

```bash
kvm-cli hid print "hello"
kvm-cli hid print "guten tag" --keymap de
```

### `kvm-cli hid set-mouse-output <mode>`
Set the mouse output mode via `POST /api/hid/set_params?mouse_output=<mode>`.
Modes: `usb`, `usb_rel`, `usb_hybrid`, `usb_touch`.

```bash
kvm-cli hid set-mouse-output usb_rel
kvm-cli hid set-mouse-output usb
```

### `kvm-cli hid mouse`
Move, click, and scroll the remote mouse.

| Subcommand | Usage | Notes |
|------------|-------|-------|
| `hid mouse move <x> <y>` | flags `--absolute` (default), `--pct`, `--relative` | Absolute pixels by default |
| `hid mouse click <left\|middle\|right\|up\|down>` | flags `--at X,Y`, `--at-pct` | Click a button; move first with `--at` |
| `hid mouse down <left\|middle\|right\|up\|down>` | | Press and hold |
| `hid mouse up <left\|middle\|right\|up\|down>` | | Release |
| `hid mouse wheel <dx> <dy>` | | Positive y scrolls up |

```bash
kvm-cli hid mouse move 1000 500
kvm-cli hid mouse move --pct 50 50
kvm-cli hid mouse move --relative 5 -5
kvm-cli hid mouse move --absolute 960 540
kvm-cli hid mouse click left
kvm-cli hid mouse click left --at 961,803      # move + click in one round trip
kvm-cli hid mouse click left --at-pct 50,50
kvm-cli hid mouse down left
kvm-cli hid mouse up left
kvm-cli hid mouse wheel 0 -3
```

---

## atx

Control the target machine's ATX power and reset buttons.

> **Caveat:** ATX is **disabled on the GL-RM1PE unit at
> `glkvm.local`** (`enabled=false` in `atx status`). Pass
> `--yes` (or `-f`/`--force`) to confirm a click; the same flag overrides the
> disabled guard, so only use it if you have wired the ATX header.

The `atx` action commands require the standard confirmation flag `--yes`, with
`-f`/`--force` as a synonym. Confirming also attempts the click when ATX reports
`enabled=false`. Use `--dry-run` to preview without touching the target.

### `kvm-cli atx status`
Show the ATX power state.

```bash
kvm-cli atx status
kvm-cli atx status --json
```

Real output:

```json
{ "enabled": false, "busy": false, "power": "off", "leds": { "power": false, "hdd": false } }
```

### `kvm-cli atx power`
Press the ATX power button. Flags: `--long` (press and hold → `power_long`) and
the required `--yes` / `-f` / `--force` confirmation.

```bash
kvm-cli atx power --yes
kvm-cli atx power --long --yes
```

### `kvm-cli atx reset`
Press the target reset button. Requires `--yes` / `-f` / `--force`.

```bash
kvm-cli atx reset --yes
```

### `kvm-cli atx click <power|power_long|reset>`
Press an explicit ATX button. Requires `--yes` / `-f` / `--force`.

```bash
kvm-cli atx click power --yes
kvm-cli atx click power_long --yes
kvm-cli atx click reset --yes
```

---

## cua (computer-use assistance)

`kvm-cli cua` turns a screenshot into a clickable UI element using the personal
**models platform** (default `https://models.example.com`). A *grounding* model
(OmniParser) parses the frame into a numbered Set-of-Mark element list; a
*planner* chat model chooses the element matching a natural-language
instruction; the element's center becomes a click coordinate.

```
screenshot ─► cua ground    POST /model/<grounding>/v1/ground   (numbered elements)
elements + instruction ─► planner   POST /model/<planner>/v1/chat/completions
planner element id ─► element center (x, y) ─► optional HID click
```

**Only the grounded element list reaches the planner — never image bytes.** The
screenshot is sent to the grounding model, but the base64 image is never placed
in the planner prompt and is never included in JSON output.

Configuration (resolved **flag > environment > config > default**, see
[docs/config.md](../../docs/config.md)):

| Setting | Flag | Environment | Config key | Default |
|---------|------|-------------|-----------|---------|
| Platform root | `--models-url` | `KVM_MODELS_URL` | `models_url` | `https://models.example.com` |
| Grounding model | `--model` | `KVM_GROUNDING_MODEL` | `grounding_model` | auto |
| Planner model | `--planner` | `KVM_PLANNER_MODEL` | `planner_model` | *(none — optional)*; `auto` picks a running chat model |
| Scratch dir | `--scratch-dir` | `KVM_SCRATCH_DIR` | `scratch_dir` | OS temp dir |

`--models-url` is persistent on the whole `cua` group. `--image`, `--annotate`,
`--model`, `--box-threshold` and `--iou-threshold` are shared by the grounding
subcommands; `--planner` is used by `cua click` (instruction mode) only. The
planner is **optional**: with none configured, instruction-mode click fails with
`PLANNER_REQUIRED` and guidance to pick the element yourself via `cua find` +
selector-mode `cua click --index/--id/--text` — the usual agent loop.

The planner can also target any OpenAI-compatible endpoint:
`--planner <model> --planner-url https://host/v1 --planner-api-key $KEY`
(env `KVM_PLANNER_URL`/`KVM_PLANNER_API_KEY`, config `planner_url`/`planner_api_key`).
`cua probe` reports the effective `planner_url` (credentials masked).

### Grounding backends

`--grounding-backend` (env `KVM_GROUNDING_BACKEND`, config `grounding_backend`)
selects where element detection runs:

| Backend | What it gives you | Needs |
|---------|-------------------|-------|
| `platform` (default) | OCR'd, labelled Set-of-Mark elements; annotated images (`--annotate`) | the models platform |
| `local` | interactive icon **boxes only** (no OCR captions) | ONNX Runtime (`brew install onnxruntime`) + the pinned model (`kvm-cli cua model download --yes`, ~12 MB, SHA-256-verified) |

Local grounding runs the OmniParser-v2.0 `icon_detect` detector (single-class
YOLOv8n, 640px letterbox, conf 0.05 / NMS IoU 0.1 — the same thresholds as the
platform) in-process via `--ort-ep auto` (CoreML on Apple Silicon, CPU
elsewhere; `--ort-ep cpu|coreml|cuda` to pin it). Elements come back as
`type: icon` with empty `content` — read the screenshot itself for semantics.
`kvm-cli cua model path` prints the cache location.

Tier 1 adds the **local Florence-2 captioner** (`--local-captions`, env
`KVM_LOCAL_CAPTIONER`, config `local_captioner`): a uv-managed Python sidecar
shipped inside kvm-cli serves `microsoft/Florence-2-base`
(`<MORE_DETAILED_CAPTION>`) with device autodetect (CUDA / MPS / CPU). Every
detected icon crop is captioned on-device, so elements carry descriptions
without OCR. Manage the sidecar with `kvm-cli cua captioner serve|status|stop`
(first start downloads ~1 GB of weights). Captions are model descriptions, not
OCR text — OCR'd text elements still require the platform tier.

**Escalation ladder** (opt-in): `--escalate on-empty` (env
`KVM_GROUNDING_ESCALATE`, config `grounding_escalate`) retries the next tier
when grounding finds zero elements: local boxes → local+florence → platform.
The final tier is reported in the output's `model` field
(`icon_detect-local`, `icon_detect-local+florence`, or the platform's model id).

The deterministic selector flags below are shared by `cua find`, `cua click` and
`cua wait`:

| Flag | Description |
|------|-------------|
| `--text S` | Match elements whose OCR content contains `S` (case-insensitive) |
| `--exact` | Require `--text` to equal the whole content |
| `--regex RE` | Case-insensitive RE2 match against the content |
| `--interactive` | Only interactive elements |
| `--region X1,Y1,X2,Y2` | Only elements whose center is inside the rectangle |
| `--nearest X,Y` | Sort matches by distance to the point |
| `--index N` | Pick the Nth match (document order), or element id N when used alone |
| `--id N` | Select the grounding element with id N (alias for a bare `--index`) |
| `--from FILE` | Reuse elements from a saved `cua ground --json` file |
| `--all` | (`find` only) print every match, not just the best |
| `--map X1,Y1,X2,Y2` + `--map-scale N` | Treat the image as a crop of the source region and map coordinates back |

Criteria AND together. `find`, `text` and `wait` never invoke the planner;
`click` reaches the planner only when no selector is supplied.

`cua ground`/`cua find`/`cua text` JSON emits the grounded elements in this shape
(the annotated base64 image is never serialized):

```jsonc
{
  "image_path": "/tmp/kvm-cua-*.jpg",
  "model": "omniparser",
  "width": 2560, "height": 1440,
  "count": 2,
  "elements": [
    {
      "type": "text",
      "interactivity": true,
      "content": "Sign in",
      "bbox": [x1, y1, x2, y2],          // pixels
      "bbox_norm": [nx1, ny1, nx2, ny2],// 0..1
      "center": [cx, cy]                 // pixels — use for clicks
    }
  ],
  "elapsed_ms": 812.4
}
```

`--jq` recipes (no external tooling needed):

```bash
kvm-cli cua ground --json --jq '.elements[].content'
kvm-cli cua ground --json --jq '[.elements[] | select(.interactivity) | .center]'
kvm-cli cua ground --json --fields image_path,count
```

### `kvm-cli cua models`
Fetch the platform catalog and list the grounding (screen-parser) and chat
(planner) models with a running/available marker. The effective grounding and
planner defaults are marked with their role.

Output columns: `ID`, `KIND`, `RUNNING`, `DEFAULT`; footer shows
`models_url`, `grounding default`, and `planner default`.

```bash
kvm-cli cua models
kvm-cli cua models --json
kvm-cli cua models --models-url https://models.example.com
```

Real output (table):

```
 ID          │ KIND      │ RUNNING │ DEFAULT
 omniparser  │ grounding │ true    │ grounding
 hemmingway  │ chat      │ true    │ -
 qwen3.8-27b │ chat      │ true    │ planner
```

### `kvm-cli cua probe` / `kvm-cli cua status`
Call the grounding model's Microsoft `omniparserserver` probe endpoint
(`GET /model/<grounding>/probe/`) and print its readiness message plus the
effective `models_url`, `grounding_model`, and `planner_model`. `cua status` is
an exact alias for `cua probe`.

```bash
kvm-cli cua probe
kvm-cli cua probe --json
kvm-cli cua status --json
```

Real output (JSON):

```json
{ "message": "Omniparser API ready", "models_url": "https://models.example.com", "grounding_model": "omniparser" }
```

### `kvm-cli cua ground [image]`
Ground an image into a numbered list of UI elements. With no image argument (and
no `--image`) a fresh KVM screenshot is captured into the scratch directory
first. The response lists each element's `type`, OCR `content`, pixel `bbox`,
normalized `bbox_norm`, and `center` point. Output columns: `ID`, `TYPE`,
`CONTENT`, `CENTER`.

Flags (shared with `cua click` unless noted):

| Flag | Default | Description |
|------|---------|-------------|
| `--model` | `omniparser` | Grounding (OmniParser) model id (env `KVM_GROUNDING_MODEL`) |
| `--planner` | auto | Planner chat model id (env `KVM_PLANNER_MODEL`); used by `click` |
| `--box-threshold` | `0.05` | OmniParser box confidence threshold |
| `--iou-threshold` | `0.1` | OmniParser IoU threshold |
| `--image` | (capture fresh) | Image file to operate on |
| `--annotate` | (off) | Write the Set-of-Mark PNG to PATH (`auto` = scratch path) |
| `--keep-image` | `true` | Ground-only: keep an auto-captured screenshot in the scratch dir |
| `--region` | (off) | Keep only elements whose center is inside `X1,Y1,X2,Y2` |
| `--interactive` | `false` | Keep only interactive elements |

The Set-of-Mark PNG is written only when `--annotate` is given; the base64 image
is never included in JSON output.

```bash
kvm-cli cua ground
kvm-cli cua ground screenshot.jpg
kvm-cli cua ground --image /tmp/screen.jpg --annotate /tmp/som.png
kvm-cli cua ground --box-threshold 0.1 --iou-threshold 0.2 --json
kvm-cli cua ground --region 0,0,1280,720 --interactive --json
```

### `kvm-cli cua find`
Ground a screenshot (or reuse elements from `--from`) and select element(s)
deterministically — **without invoking the planner**. At least one selector is
required. By default only the best match is printed; `--all` prints every match.
JSON output carries `image_path`, `selector`, `match_count`, `matches[]`
(`id/type/content/interactivity/bbox/center[/distance]`), `best`, and `click_x`/
`click_y`.

```bash
kvm-cli cua find --text "Sign in"
kvm-cli cua find --text "Okta" --region 700,560,1250,760 --all
kvm-cli cua find --regex "ok|cancel" --all
kvm-cli cua find --text "Next" --nearest 961,681
kvm-cli cua find --id 135
kvm-cli cua find --from /tmp/g.json --text Next
# Read a small crop and map it back to source coordinates:
kvm-cli cua find --image /tmp/crop.jpg --map 700,560,1250,760 --map-scale 2 --text Next
```

No match exits non-zero with a `NO_MATCH` coded error.

### `kvm-cli cua text`
Ground an image (or reuse `--from`) and print only the numbered element list —
`ID`, `TYPE`, `CONTENT`, `CENTER` — with no planner and no annotated image. The
fastest way to read the screen as text; `--plaintext` is pipe-friendly. Accepts
`--image`, `--from`, `--region`, and `--interactive`.

```bash
kvm-cli cua text
kvm-cli cua text --plaintext
kvm-cli cua text --from /tmp/g.json --json
```

### `kvm-cli cua wait`
Poll screenshot→ground until a selector matches, or (with `--gone`) until it
stops matching. Requires `--text` or `--regex`. Flags: `--gone`, `--max-wait`
(default `30s`; used instead of `--timeout` to avoid clashing with the global
HTTP timeout) and `--interval` (default `1s`). Exits 0 on success and returns a
`TIMEOUT` coded error on the deadline.

```bash
kvm-cli cua wait --text "Push notification sent"
kvm-cli cua wait --text "Loading" --gone --max-wait 60s
kvm-cli cua wait --regex "signed in|dashboard" --json
```

### `kvm-cli cua click [INSTRUCTION]`
Resolve a target to a click coordinate and, optionally, click it. Two modes:

- **Planner mode** (default, `INSTRUCTION` given): capture a screenshot (unless
  `--image` is given) → ground it into numbered elements → ask the planner which
  element matches `INSTRUCTION` → resolve that element's center.
- **Selector mode** (a selector flag is given, no `INSTRUCTION`): the element is
  chosen locally by `--text`/`--regex`/`--index`/`--id`/`--from`/`--region`/
  `--interactive`/`--nearest`; the planner is skipped. Faster and deterministic.

By default nothing is clicked; the chosen element and click `(x, y)` are printed.
Output is a `FIELD`/`VALUE` table with `element_id`, `element_type`,
`element_content`, `click`, `screenshot`, `ground_ms`, `plan_ms`, `executed`
(plus `instruction` in planner mode, `match_count` in selector mode, and
`annotated` when `--annotate` is set).

Write gate: `--execute` requires `--yes` (or its synonyms `-f`/`--force`) and is
previewable with the global `--dry-run`. `--execute` without `--yes` is refused.
`--region`/`--interactive` also pre-filter the element set handed to the planner.

```bash
kvm-cli cua click "click the Settings icon"
kvm-cli cua click "open the terminal"
kvm-cli cua click "click Login" --image /tmp/screen.jpg --json
kvm-cli cua click --text "Sign in" --execute --yes
kvm-cli cua click --text "Next" --region 1300,150,1850,400 --json
kvm-cli cua click --from /tmp/g.json --id 135 --execute --yes
kvm-cli cua click "click OK" --execute --dry-run   # preview the click
```

### `kvm-cli cua parse [image]`
Parse an image through the Microsoft `omniparserserver`-compatible `/parse/`
endpoint (`POST /model/<grounding>/parse/`) and print the parsed content list:
`type`, normalized `bbox`, OCR `content`. With no image argument (and no
`--image`) a fresh screenshot is captured into the scratch directory first.

Flags: `--image` (default: capture fresh) and `--annotate` (write the returned
Set-of-Mark PNG; `auto` = scratch path). Output columns: `TYPE`, `BBOX`,
`CONTENT`.

```bash
kvm-cli cua parse screenshot.jpg
kvm-cli cua parse
kvm-cli cua parse screenshot.jpg --annotate /tmp/som.png --json
```

---

## vnc

Drive a machine over VNC (RFB) with no external tools. Authentication: None,
VNC DES, and Apple/ARD Diffie-Hellman (macOS Screen Sharing). Encodings: RAW,
CopyRect, Hextile, and the DesktopSize pseudo-encoding.

Connection settings are persistent on the `vnc` group (flag > environment):

| Flag | Env | Description |
|------|-----|-------------|
| `--host` | `KVM_VNC_HOST` / `VNC_HOST` | `HOST` or `HOST:PORT` (default port 5900) |
| `--username` | `KVM_VNC_USERNAME` / `VNC_USERNAME` | Required for Apple ARD auth |
| `--password` | `KVM_VNC_PASSWORD` / `VNC_PASSWORD` | VNC password |
| `--vnc-timeout` | | Dial/read timeout (default 15s) |

### `kvm-cli vnc info`
Connect and print the desktop name and framebuffer size.

```bash
kvm-cli vnc --host HOST:PORT --password "$PW" info
```

### `kvm-cli vnc screenshot`
Capture the framebuffer to a PNG (default: a timestamped file in the scratch dir;
`-o -` writes to stdout).

```bash
kvm-cli vnc --host HOST:PORT --password "$PW" screenshot -o /tmp/v.png
kvm-cli vnc --host HOST:PORT --password "$PW" screenshot -o - > /tmp/v.png
```

### `kvm-cli vnc mouse {move,click,down,up,wheel}`
Pointer control. `move X Y` takes absolute pixels (`--pct` for percentages);
`click BTN` accepts `left|middle|right|up|down` and `--at X,Y`/`--at-pct` to move
first; `wheel DX DY` (positive dy scrolls up).

```bash
kvm-cli vnc --host HOST:PORT --password "$PW" mouse move 100 200
kvm-cli vnc --host HOST:PORT --password "$PW" mouse click left --at 490,358
```

### `kvm-cli vnc key NAME`
Press a key by name (`enter`, `tab`, `escape`, `ctrl`, `f5`, ...) or a `+` combo
(`ctrl+a`).

```bash
kvm-cli vnc --host HOST:PORT --password "$PW" key enter
kvm-cli vnc --host HOST:PORT --password "$PW" key ctrl+a
```

### `kvm-cli vnc type TEXT`
Type a string (`--enter` appends Enter).

```bash
kvm-cli vnc --host HOST:PORT --password "$PW" type "hello" --enter
```

### `--vnc` integration on `screenshot` and `cua`
`screenshot --vnc HOST[:PORT]` captures over VNC (PNG, supports `--region`/`--scale`).
Every `cua` subcommand accepts `--vnc`/`--vnc-username`/`--vnc-password`, so
grounding, `cua find`/`text`/`wait`, and `cua click --execute` run over VNC.

```bash
kvm-cli screenshot --vnc HOST:PORT --vnc-password "$PW" -o /tmp/v.png
kvm-cli cua click --vnc HOST:PORT --vnc-password "$PW" --text "Next" --execute --yes
```

---

## rustdesk

Manage local RustDesk state and launch connections. RustDesk has no headless
screenshot/input API, so kvm-cli does not re-implement its protocol; it manages
config/peers and launches sessions, which are then driven through a display
transport (KVM or VNC). Config directory defaults to `~/.config/rustdesk`
(Linux) or `~/Library/Preferences/com.carriez.RustDesk` (macOS); override with
`--config-dir`. Passwords are never printed.

| Command | Description |
|---------|-------------|
| `rustdesk info` | id/`has_id`, relay server, permanent-password status |
| `rustdesk id` | plaintext id via the client (`rustdesk --get-id`), else config |
| `rustdesk peers` | address-book entries (id, alias, hostname, username, platform) |
| `rustdesk status` | whether a local rustdesk process is running |
| `rustdesk connect ID` | launch `rustdesk --connect ID` (write; requires `--yes`) |

```bash
kvm-cli rustdesk info
kvm-cli rustdesk id
kvm-cli rustdesk peers --json
kvm-cli rustdesk connect 123456789 --yes
kvm-cli rustdesk connect 123456789 --dry-run
```

To drive a RustDesk session on a Linux host, run it on a display (e.g. `:1`) and
point the VNC transport at that display.

---

## msd

Manage the remote KVM's virtual media (Mass Storage Device) emulation.

### `kvm-cli msd status`
Show whether virtual media is enabled/online, whether the drive is connected,
the mounted image, and free/used storage.

```bash
kvm-cli msd status
kvm-cli msd status --json
```

### `kvm-cli msd partitions`
List device storage partitions, including filesystem, size, label, UUID, and
which one is current.

```bash
kvm-cli msd partitions
kvm-cli msd partitions --json
```

### `kvm-cli msd connect <image>`
Mount a partition or image (e.g. `/dev/disk/by-uuid/XXXX`) so the target sees it
as removable media.

```bash
kvm-cli msd connect /dev/disk/by-uuid/0717-B213
```

### `kvm-cli msd disconnect`
Disconnect the currently mounted partition/image from the target.

```bash
kvm-cli msd disconnect
```

### `kvm-cli msd set-connected <true|false>`
Attach (`true`) or detach (`false`) the virtual U-disk/CD-ROM without changing
the selected image.

```bash
kvm-cli msd set-connected true
kvm-cli msd set-connected false
```

### `kvm-cli msd set-params <key=value>...`
Set one or more virtual-drive parameters. Flags: `--cdrom` (set `cdrom=true`),
`--image <path>` (set `image=<path>`).

```bash
kvm-cli msd set-params cdrom=true
kvm-cli msd set-params --image /dev/block/by-name/media
kvm-cli msd set-params --cdrom=false image=ubuntu.iso
```

### `kvm-cli msd write <file>`
Upload a local disk image (typically `.iso`) to the device's MSD storage.
Destructive: requires `--yes`.

Flags: `--image <name>` (default: base name of `<file>`), `--prefix <path>`,
`--remove-incomplete` (default `true`), `--yes`.

```bash
kvm-cli msd write ./ubuntu.iso --yes
kvm-cli msd write ./disk.img --image backup.img --yes
kvm-cli msd write ./disk.img --prefix iso/ --yes
```

### `kvm-cli msd write-remote <url>`
Ask the device to download an image from `<url>` into MSD storage. Requires
`--yes`.

Flags: `--prefix <path>`, `--remove-incomplete` (default `true`), `--yes`.

```bash
kvm-cli msd write-remote https://example.com/win11.iso --yes
```

### `kvm-cli msd format [path]`
Format the virtual-media partition, erasing its contents. Requires `--yes`.

```bash
kvm-cli msd format --yes
kvm-cli msd format /dev/mmcblk0p10 --yes
```

### `kvm-cli msd remove <image>`
Delete the named image from MSD storage. Requires `--yes`; without it, prints a
dry run.

```bash
kvm-cli msd remove ubuntu.iso --yes
kvm-cli msd remove ubuntu.iso            # dry run
```

---

## wol

Manage the remote KVM's Wake-on-LAN support.

### `kvm-cli wol list`
List stored MAC addresses with names and broadcast/port details.

```bash
kvm-cli wol list
kvm-cli wol list --json
```

### `kvm-cli wol scan`
Scan the local network for devices that could be woken.

```bash
kvm-cli wol scan
kvm-cli wol scan --json
```

### `kvm-cli wol add <mac> [name]`
Store a MAC address (and optional friendly name) for later wake-ups.

```bash
kvm-cli wol add AA:BB:CC:DD:EE:FF
kvm-cli wol add AA:BB:CC:DD:EE:FF workstation
```

### `kvm-cli wol wake <mac>`
Send a Wake-on-LAN magic packet to the given MAC address.

```bash
kvm-cli wol wake AA:BB:CC:DD:EE:FF
```

### `kvm-cli wol remove <mac>`
Delete a stored MAC address. Destructive: requires `--yes`; otherwise prints a
dry run.

```bash
kvm-cli wol remove AA:BB:CC:DD:EE:FF --yes
kvm-cli wol remove AA:BB:CC:DD:EE:FF          # dry run
```

---

## switch

Inspect and control the device's USB switch ports. **Not supported on this
firmware** — the endpoint returns `HTTP 404` and the command reports
`USB switch control is not supported on this device` (exit 1).

### `kvm-cli switch status`
Show available USB switch ports and which is active.

```bash
kvm-cli switch status
kvm-cli switch status --json
```

### `kvm-cli switch set-active <port>`
Switch the active USB port to `<port>`.

```bash
kvm-cli switch set-active 1
```

---

## screen

Configure the remote KVM's custom screen (wallpaper, clock, mode). Also
available under the aliases `custom-screen` and `custom_screen`. **Not
supported on this firmware** — commands report a clear "not supported" message.

### `kvm-cli screen status`
Show the current screen mode, date format, and time format.

```bash
kvm-cli screen status
kvm-cli screen status --json
```

### `kvm-cli screen background`
Fetch the current background (base64-decoded with `--output`).
Flag: `--output` / `-o` — decode to this file.

```bash
kvm-cli screen background
kvm-cli screen background --output wallpaper.png
```

### `kvm-cli screen set-background <file>`
Upload `<file>` as the background. Requires `--yes`.

```bash
kvm-cli screen set-background ./wallpaper.png --yes
```

### `kvm-cli screen delete-background`
Remove the current background. Requires `--yes`.

```bash
kvm-cli screen delete-background --yes
```

### `kvm-cli screen set-mode <mode>`
Values: `default` (world clock), `clock_only`, `wallpaper_only`.

```bash
kvm-cli screen set-mode clock_only
```

### `kvm-cli screen set-time-format <format>`
Values: `24h`, `12h`.

```bash
kvm-cli screen set-time-format 24h
```

### `kvm-cli screen set-date-format <format>`
Values: `locale`, `mm_dd_yyyy`, `dd_mm_yyyy`, `yyyy_mm_dd`.

```bash
kvm-cli screen set-date-format yyyy_mm_dd
```

---

## tailscale

Manage the device's Tailscale client.

### `kvm-cli tailscale status`
Report the Tailscale daemon's running state.

```bash
kvm-cli tailscale status
kvm-cli tailscale status --json
```

### `kvm-cli tailscale login-url`
Ask the device for the URL to open to authorize it on your tailnet.

```bash
kvm-cli tailscale login-url
kvm-cli tailscale login-url --json
```

### `kvm-cli tailscale login`
Query the device's login endpoint. Reports `NOT_SUPPORTED` when the firmware
does not implement it — use `login-url` instead.

```bash
kvm-cli tailscale login
```

### `kvm-cli tailscale login-status`
Show the tailnet identity (IPv4/IPv6, login name) and backend state.

```bash
kvm-cli tailscale login-status
kvm-cli tailscale login-status --json
```

### `kvm-cli tailscale start`
Start the Tailscale daemon.

```bash
kvm-cli tailscale start
```

### `kvm-cli tailscale stop`
Stop the daemon (disconnect the device). Requires `--yes`; otherwise dry run.

```bash
kvm-cli tailscale stop --yes
kvm-cli tailscale stop          # dry run
```

### `kvm-cli tailscale logout`
Unbind the device from your tailnet. Requires `--yes`.

```bash
kvm-cli tailscale logout --yes
```

### `kvm-cli tailscale config`
Read the Tailscale configuration, or write with flags. Omitting all flags reads.

Flags: `--advertise-routes <auto\|cidr,...>`, `--exit-node <true/false/address>`,
`--set key=value` (repeatable).

```bash
kvm-cli tailscale config
kvm-cli tailscale config --exit-node true
kvm-cli tailscale config --advertise-routes auto
kvm-cli tailscale config --set accept_dns=true
```

---

## netbird

Manage the device's NetBird client.

| Command | Description | Example |
|---------|-------------|---------|
| `netbird info` | Running/connected state, assigned IP, error message | `kvm-cli netbird info --json` |
| `netbird login` | Start the login flow | `kvm-cli netbird login` |
| `netbird start` | Start the daemon | `kvm-cli netbird start` |
| `netbird stop` | Stop the daemon (requires `--yes`) | `kvm-cli netbird stop --yes` |
| `netbird logout` | Log out (requires `--yes`) | `kvm-cli netbird logout --yes` |

```bash
kvm-cli netbird info
kvm-cli netbird login
kvm-cli netbird start
kvm-cli netbird stop --yes
kvm-cli netbird logout --yes
```

---

## zerotier

Manage the device's ZeroTier client.

### `kvm-cli zerotier status`
Show whether ZeroTier is enabled and whether its process is running.

```bash
kvm-cli zerotier status
kvm-cli zerotier status --json
```

### `kvm-cli zerotier set-token <network-id>`
Store the 16-character ZeroTier network ID the device should join.

```bash
kvm-cli zerotier set-token 8056c2e21c000001
```

### `kvm-cli zerotier start`
Start the ZeroTier daemon.

```bash
kvm-cli zerotier start
```

### `kvm-cli zerotier stop`
Stop the daemon (disconnect). Requires `--yes`.

```bash
kvm-cli zerotier stop --yes
kvm-cli zerotier stop          # dry run
```

---

## repeater

Manage the device's Wi-Fi repeater/station mode.

### `kvm-cli repeater scan`
Scan for Wi-Fi access points and saved networks (can take up to 30 seconds).

```bash
kvm-cli repeater scan
kvm-cli repeater scan --json
```

### `kvm-cli repeater status`
Show the access point the repeater is currently connected to.

```bash
kvm-cli repeater status
```

### `kvm-cli repeater saved`
List the saved access points.

```bash
kvm-cli repeater saved
```

### `kvm-cli repeater connect <ssid>`
Connect the repeater to `<ssid>`. Flags: `--key <passphrase>`,
`--identity <eap-identity>`, `--manual`.

```bash
kvm-cli repeater connect MyNetwork --key secret
kvm-cli repeater connect CorpWiFi --key secret --identity user@example.com
kvm-cli repeater connect OpenNetwork
```

### `kvm-cli repeater enable <true|false>`
Turn the repeater/station mode on or off.

```bash
kvm-cli repeater enable true
kvm-cli repeater enable false
```

### `kvm-cli repeater disconnect`
Disconnect the repeater. Requires `--yes`.

```bash
kvm-cli repeater disconnect --yes
```

### `kvm-cli repeater remove-saved <ssid>`
Remove a saved access point. Requires `--yes`.

```bash
kvm-cli repeater remove-saved MyNetwork --yes
```

---

## ap

Manage the device's built-in Wi-Fi access point (hotspot).

### `kvm-cli ap status`
Show the hotspot configuration and state.

```bash
kvm-cli ap status
kvm-cli ap status --json
```

### `kvm-cli ap enable`
Turn the hotspot on or off. Flags: `--enable` (default `true`),
`--set key=value` (repeatable extra config).

```bash
kvm-cli ap enable
kvm-cli ap enable --enable=false
kvm-cli ap enable --set ssid=my-hotspot --set key=secret
```

### `kvm-cli ap close-all`
Disable every active access-point mode.

```bash
kvm-cli ap close-all
```

### `kvm-cli ap open-last`
Re-open the hotspot using the last-used configuration.

```bash
kvm-cli ap open-last
```

---

## modem

Manage the device's cellular modem.

### `kvm-cli modem at <command>`
Send an AT command to the modem and print its response.

```bash
kvm-cli modem at "AT+CGMI"
kvm-cli modem at "AT+CSQ" --json
```

### `kvm-cli modem sim-setting`
Read the modem's SIM settings (`GET`). Flag: `--set key=value` (repeatable) —
supplying it writes instead of reads.

```bash
kvm-cli modem sim-setting
kvm-cli modem sim-setting --set apn=internet
cat sim.json | kvm-cli modem sim-setting --json
```

### `kvm-cli modem input-pin <pin>`
Submit the SIM PIN to unlock the SIM card.

```bash
kvm-cli modem input-pin 1234
```

---

## upgrade

Inspect and manage the remote KVM's firmware.

### `kvm-cli upgrade version`
Show the device model and installed firmware version.

```bash
kvm-cli upgrade version
kvm-cli upgrade version --json
```

Real output: `{ "model": "RM1PE", "version": "V1.10.1 release2" }`.

### `kvm-cli upgrade check`
Compare the installed firmware against the latest release. Alias: `compare`.

```bash
kvm-cli upgrade check
kvm-cli upgrade check --json
kvm-cli upgrade check --json --jq .server_version
```

### `kvm-cli upgrade status`
Show bytes downloaded / total size for an in-progress download.
Alias: `download-info`.

```bash
kvm-cli upgrade status
kvm-cli upgrade status --json
```

### `kvm-cli upgrade download`
Ask the device to download the latest firmware from the update server.

```bash
kvm-cli upgrade download
kvm-cli upgrade download --timeout 10m
```

### `kvm-cli upgrade cancel`
Abort the in-progress download and discard the partial image.
Alias: `download-cancel`.

```bash
kvm-cli upgrade cancel
kvm-cli upgrade download-cancel
```

### `kvm-cli upgrade edid`
Show the EDID block the device presents to the target, hex-encoded.
Alias: `get-edid`.

```bash
kvm-cli upgrade edid
kvm-cli upgrade get-edid --json
```

### `kvm-cli upgrade log`
Download the device log bundle.
Flag: `--output` / `-o` (default `kvm-upgrade-log.zip`, `-` for stdout).

```bash
kvm-cli upgrade log
kvm-cli upgrade log --output device-log.zip
kvm-cli upgrade log --output - > log.zip
```

### `kvm-cli upgrade start`
Install the firmware previously fetched with `download`. Requires `--yes`.
Flags: `--save-config` (default `true`), `--yes`.

```bash
kvm-cli upgrade start --yes
kvm-cli upgrade start --save-config=false --yes
```

### `kvm-cli upgrade upload <file>`
Upload a local firmware image and start the install. Requires `--yes`.

```bash
kvm-cli upgrade upload ./RM1PE-V1.10.1.img --yes
```

### `kvm-cli upgrade reboot`
Reboot the remote KVM (device becomes temporarily unreachable). Requires
`--yes`.

```bash
kvm-cli upgrade reboot --yes
```

### `kvm-cli upgrade reset`
Restore factory defaults, wiping all configuration. Requires `--yes`.

```bash
kvm-cli upgrade reset --yes
```

---

## twofa (2fa)

Manage the remote KVM's two-factor authentication. The group is also invocable
as `2fa`.

### `kvm-cli 2fa is-enabled`
Report whether 2FA is enabled (`GET /api/2fa/is_enabled`).

```bash
kvm-cli 2fa is-enabled
kvm-cli 2fa is-enabled --json
```

### `kvm-cli 2fa show`
Show the device's current 2FA document.

```bash
kvm-cli 2fa show
kvm-cli 2fa show --json
```

### `kvm-cli 2fa create`
Generate a new TOTP secret on the device. Requires `--yes`.

```bash
kvm-cli 2fa create --yes
```

### `kvm-cli 2fa init`
Activate 2FA by confirming a generated secret with a current code. Requires
`--yes`. Flags: `--secret` (env `KVM_2FA_SECRET`; prompted if omitted),
`--code`.

```bash
kvm-cli 2fa init --secret JBSWY3DPEHPK3PXP --code 123456 --yes
```

### `kvm-cli 2fa delete`
Disable 2FA and discard the stored secret. Requires `--yes`.

```bash
kvm-cli 2fa delete --yes
```

---

## asr

Inspect and control automatic speech recognition. **Not available on this
device** — commands report a clear message (exit 1).

| Command | Description | Example |
|---------|-------------|---------|
| `asr status` | Whether ASR is running | `kvm-cli asr status --json` |
| `asr start` | Start ASR (no `--yes` needed) | `kvm-cli asr start` |
| `asr stop` | Stop ASR (no `--yes` needed) | `kvm-cli asr stop` |

---

## fingerbot

Inspect and control the attached fingerbot (an actuator that presses a physical
button). **Not available on this device** when no fingerbot is attached.

### `kvm-cli fingerbot battery`
Read the fingerbot's battery level.

```bash
kvm-cli fingerbot battery
kvm-cli fingerbot battery --json
```

### `kvm-cli fingerbot click`
Press the fingerbot for a given duration and strength.
Flags: `--press-time <ms>` (default `500`; `500` or `1000`–`60000`),
`--strength <low|high>` (default `high`).

```bash
kvm-cli fingerbot click
kvm-cli fingerbot click --press-time 3000 --strength high
kvm-cli fingerbot click --press-time 500 --strength low
```

### `kvm-cli fingerbot upgrade`
Flash the fingerbot's firmware. Requires `--yes`.

```bash
kvm-cli fingerbot upgrade --yes
```

---

## init

Inspect the remote KVM's initialization state and manage its admin password.

### `kvm-cli init status`
Report whether first-run initialization is complete, plus screen/country
settings. Alias: `is-inited`.

```bash
kvm-cli init status
kvm-cli init is-inited --json
```

### `kvm-cli init run`
Perform first-run initialization, setting the admin password. Alias: `init`.
Requires `--yes`. Flag: `--password` (env `KVM_NEW_PASSWORD`; prompted if
omitted).

```bash
kvm-cli init run --password 'correct horse battery staple' --yes
kvm-cli init init --yes   # prompt for the password
```

### `kvm-cli init change-password`
Change the admin password. Requires `--yes`.
Flags: `--old-password` (env `KVM_OLD_PASSWORD`; prompted if omitted),
`--new-password` (env `KVM_NEW_PASSWORD`; prompted if omitted),
`--user` (default `admin`).

```bash
kvm-cli init change-password --yes
kvm-cli init change-password --old-password old --new-password new --yes
```

---

## turn

### `kvm-cli turn get`
Show the TURN server username, credential, TTL, and relay URIs.

```bash
kvm-cli turn get
kvm-cli turn get --json
```

---

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | User/correctable or runtime error (auth failure, missing config, unsupported endpoint) |
| `2` | Usage error (unknown flag/command, invalid arguments) |

## Output conventions

- **stdout** = data only; **stderr** = warnings, progress, and errors.
- In `--json` mode, errors are emitted to stderr as
  `{"error": {"code": "...", "message": "..."}}` with a stable `code`
  (`AUTH_INVALID`, `FORBIDDEN`, `NOT_FOUND`, `NETWORK_ERROR`, `USAGE`, `ERROR`).
- `--fields` projects JSON output; `--jq` filters it.
