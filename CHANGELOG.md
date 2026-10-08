# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0]

### Added

- **Local Florence-2 captioner (grounding tier 1)**: `--grounding-backend local
  --local-captions` captions every YOLO-detected icon crop on-device using
  `microsoft/Florence-2-base` (`<MORE_DETAILED_CAPTION>`). The captioner is a
  uv-managed Python sidecar shipped inside kvm-cli
  (`kvm-cli cua captioner serve|status|stop`) with device autodetect
  (CUDA on NVIDIA, MPS on Apple Silicon, CPU fallback). Elements get model
  descriptions in `content` — no OCR; text elements still require the platform.
- **Opt-in escalation ladder**: `--escalate on-empty`
  (env `KVM_GROUNDING_ESCALATE`, config `grounding_escalate`) retries the next
  tier when grounding finds zero elements: local boxes → local+florence →
  platform. The tier that produced the final result is reported in `model`.

## [0.5.0]

### Added

- **Local grounding backend**: `--grounding-backend local` runs OmniParser-v2.0's
  `icon_detect` detector (a single-class YOLOv8n, ~12 MB ONNX) in-process via
  ONNX Runtime (purego dlopen — the binary stays CGO-free). Apple Silicon uses
  the CoreML execution provider by default; `--ort-ep cpu|coreml|cuda` pins it.
  `kvm-cli cua model download --yes` fetches the size- and SHA-256-pinned model;
  `kvm-cli cua model path` prints the cache location. Local grounding returns
  interactive icon boxes without OCR captions.
- **Planner on any OpenAI-compatible endpoint**: `--planner-url` /
  `KVM_PLANNER_URL` / `config set planner_url` (+ `--planner-api-key` /
  `planner_api_key`, sent as a Bearer token). A bare base URL gets
  `/chat/completions` appended. With `--grounding-backend local`, the platform
  is never contacted: instruction-mode clicks pair local grounding with the
  external planner endpoint directly (`--planner <id> --planner-url <url>`).

### Changed

- `cua probe` reports the effective `planner_url` (credentials masked) and the
  new backend/env keys are documented in docs/config.md.

## [0.4.3]

### Changed

- The CUA **planner is now optional (opt-in)**. By default `cua` runs
  planner-free: the calling agent reads the grounded element list
  (`cua find`/`cua text`) and clicks via a selector
  (`cua click --index/--id/--text`). Instruction-mode `cua click` without a
  planner returns a recoverable `PLANNER_REQUIRED` error with that guidance.
- Opt in with `--planner auto` (or `KVM_PLANNER_MODEL=auto` /
  `config set planner_model auto`) to restore the previous behavior of picking
  a running chat model from the catalog, or name a specific model.
- The models platform now only requires a grounding model (OmniParser).
- Instruction-mode `cua click` fails fast with `PLANNER_REQUIRED` before any
  network call when no planner is configured.

## [0.4.2]

### Security

- Replaced a real RustDesk id used in examples, tests, and embedded skill
  documentation with a placeholder (123456789). No credentials were ever
  present; this removes a device identifier from the docs and binaries.

## [0.4.1]

### Fixed

- `rustdesk connect --dry-run` no longer requires the RustDesk binary to be
  installed; the preview uses the bare command name when the app is absent.
- Test hermeticity on machines without RustDesk and on Linux (connection-reset
  timing in the VNC tests).


### Added

- Native VNC (RFB) transport: `kvm-cli vnc info|screenshot|mouse|key|type`, with
  None / VNC-DES / Apple (ARD) authentication and RAW/CopyRect/Hextile decoding.
- `--vnc` integration on `screenshot` and every `cua` subcommand.
- `rustdesk` command group: `info`, `id`, `peers`, `status`, `connect`.

## [0.3.1]

### Added

- Agent ergonomics: `cua find`/`text`/`wait`, selector-mode `cua click`,
  `screenshot --region`/`--scale`/`--until-stable`, and coordinate mapping.
