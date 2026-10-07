# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
