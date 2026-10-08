# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
  `/chat/completions` appended.

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
