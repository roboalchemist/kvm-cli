#!/usr/bin/env bash
#
# : credential-free, network-free smoke tests for kvm-cli.
#
# This script validates that the kvm-cli binary builds (when a Go toolchain is
# available) and that every command/subcommand in the tree runs `--help`
# successfully, plus a handful of static commands (version, docs, completion,
# skill) and negative cases (unknown command/flag).
#
# It is designed to run under a hermetic environment, e.g.:
#     env -i PATH=/usr/bin:/bin HOME=/tmp/home make test-smoke
#
# Guarantees:
#   * No credentials required. All KVM_* / GLKVM_* env vars are cleared.
#   * No network calls. Only `--help`, static output commands, and isolated
#     `config list` are executed; device commands are only invoked with --help.
#   * Uses only utilities available in /usr/bin and /bin.
#   * Compatible with the bash 3.2 shipped with macOS (/bin/bash).
#
# Environment knobs:
#   KVM_BIN              Path to the binary (default: <repo>/kvm-cli)
#   KVM_SMOKE_REBUILD=1  Force a rebuild even if the binary already exists
#   SMOKE_NO_COLOR=1     Disable colored PASS/FAIL output (auto-detected)
#
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd)"
BIN="${KVM_BIN:-$ROOT_DIR/kvm-cli}"

# ---------------------------------------------------------------------------
# Credential / network isolation
# ---------------------------------------------------------------------------
# Make the smoke suite credential-free even when run from an interactive shell
# that happens to have device settings exported.
unset KVM_URL GLKVM_URL KVM_USERNAME GLKVM_USERNAME \
      KVM_PASSWORD GLKVM_PASSWORD KVM_TOKEN GLKVM_TOKEN \
      KVM_INSECURE KVM_CONFIG GLKVM_CONFIG 2>/dev/null || true
export NO_COLOR=1
export KVM_TIMEOUT=5s

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------
if [ -t 1 ] && [ "${SMOKE_NO_COLOR:-0}" != "1" ] && [ "${NO_COLOR:-}" = "" ]; then
  C_GREEN="$(printf '\033[32m')"; C_RED="$(printf '\033[31m')"
  C_OFF="$(printf '\033[0m')"
else
  C_GREEN=""; C_RED=""; C_OFF=""
fi

PASS=0
FAIL=0
FAILED=""

pass() { PASS=$((PASS + 1)); printf '  %sPASS%s  %s\n' "$C_GREEN" "$C_OFF" "$1"; }
fail() {
  FAIL=$((FAIL + 1))
  FAILED="${FAILED}  - $1"$'\n'
  printf '  %sFAIL%s  %s\n' "$C_RED" "$C_OFF" "$1"
}

section() { printf '\n== %s ==\n' "$1"; }

# ---------------------------------------------------------------------------
# Build (best effort)
# ---------------------------------------------------------------------------
# Locate a Go toolchain without relying on it being on PATH (it frequently is
# not in hermetic CI/agent environments).
find_go() {
  if command -v go >/dev/null 2>&1; then command -v go; return 0; fi
  local cand
  for cand in \
      /usr/local/go/bin/go \
      /opt/homebrew/bin/go \
      /opt/local/bin/go \
      /usr/local/bin/go \
      /snap/bin/go \
      "${HOME:-}/go/bin/go"; do
    if [ -n "$cand" ] && [ -x "$cand" ]; then printf '%s\n' "$cand"; return 0; fi
  done
  # Homebrew Cellar globs (versioned installs).
  for cand in /opt/homebrew/Cellar/go/*/bin/go /usr/local/Cellar/go/*/bin/go; do
    [ -x "$cand" ] && { printf '%s\n' "$cand"; return 0; }
  done
  return 1
}

printf '=== kvm-cli smoke tests (credential-free, offline) ===\n'

if [ -x "$BIN" ] && [ "${KVM_SMOKE_REBUILD:-0}" != "1" ]; then
  printf 'Binary: %s (pre-built, skipping rebuild)\n' "$BIN"
else
  GO_BIN="$(find_go || true)"
  if [ -n "${GO_BIN:-}" ]; then
    printf 'Building %s with %s ...\n' "$BIN" "$GO_BIN"
    if ! (cd "$ROOT_DIR" && "$GO_BIN" build -o "$BIN" .); then
      printf 'ERROR: go build failed\n' >&2
      exit 1
    fi
  elif [ -x "$BIN" ]; then
    printf 'Binary: %s (go toolchain not found; using existing binary)\n' "$BIN"
  else
    printf 'ERROR: go toolchain not found and no pre-built binary at %s\n' "$BIN" >&2
    exit 1
  fi
fi

if [ ! -x "$BIN" ]; then
  printf 'ERROR: %s is not executable\n' "$BIN" >&2
  exit 1
fi

# Run "$BIN" with args and capture combined output. Sets RC and OUT.
run() {
  OUT="$("$BIN" "$@" 2>&1)"
  RC=$?
}

# Assert exit code 0 for the given label + args.
assert_ok() {
  local label="$1"; shift
  run "$@"
  if [ "$RC" -eq 0 ]; then
    pass "$label"
  else
    fail "$label (exit $RC): $(printf '%s' "$OUT" | head -n1)"
  fi
}

# Assert exit code 0 and that output contains a needle.
assert_ok_contains() {
  local label="$1" needle="$2"; shift 2
  run "$@"
  if [ "$RC" -ne 0 ]; then
    fail "$label (exit $RC): $(printf '%s' "$OUT" | head -n1)"
  elif printf '%s' "$OUT" | grep -q "$needle"; then
    pass "$label"
  else
    fail "$label (output missing '$needle')"
  fi
}

# ---------------------------------------------------------------------------
# Global-flag behavior: subcommand --version / --dry-run ()
# ---------------------------------------------------------------------------
# Both are advertised as global (persistent) flags.  makes the CLI honour
# them on every command:
#   * `kvm-cli <subcommand> --version` prints the version and exits 0 WITHOUT
#     running the command (and without contacting the device).
#   * `kvm-cli <write-command> --dry-run` prints a preview, exits 0, and never
#     contacts the device.
# These are hard assertions: a regression must fail the suite.

# Device-contact failure signature (the unreachable test URL yields
# "connection refused"; auth/login failures are caught too).
DEVICE_CONTACT_RE='connection refused|dial tcp|no route to host|login to .* failed|i/o timeout'
# Dry-run preview signature.
DRY_RUN_RE='dry run|would '

# Assert `<subcommand> --version` prints the version and does NOT execute.
assert_subcmd_version() {
  local label="$1"; shift
  run "$@"
  if [ "$RC" -eq 0 ] && printf '%s' "$OUT" | grep -q '^kvm-cli '; then
    pass "$label"
  elif printf '%s' "$OUT" | grep -Eqi "$DEVICE_CONTACT_RE"; then
    fail "$label (--version ignored: command executed and contacted the device)"
  else
    fail "$label (exit $RC): $(printf '%s' "$OUT" | head -n1)"
  fi
}

# Assert `<write command> --dry-run` previews without touching the device.
assert_dry_run() {
  local label="$1"; shift
  run "$@"
  if [ "$RC" -eq 0 ] && printf '%s' "$OUT" | grep -Eqi "$DRY_RUN_RE"; then
    pass "$label"
  elif printf '%s' "$OUT" | grep -Eqi "$DEVICE_CONTACT_RE"; then
    fail "$label (--dry-run ignored: device contacted)"
  else
    fail "$label (exit $RC): $(printf '%s' "$OUT" | head -n1)"
  fi
}

# ---------------------------------------------------------------------------
# Basic commands
# ---------------------------------------------------------------------------
section "Basic commands"
assert_ok "--help" --help
assert_ok "-h" -h
assert_ok "--version" --version
assert_ok "-V" -V
assert_ok_contains "--version prints name/version" '^kvm-cli ' --version
assert_ok "version" version
assert_ok_contains "docs" 'kvm-cli' docs
assert_ok "help" help

# ---------------------------------------------------------------------------
# Global flags on subcommands: --version / --dry-run
# ---------------------------------------------------------------------------
section "Subcommand --version (must not execute)"
# Point at an unreachable URL so that any attempt to actually run the command
# fails with a device-contact error -- exactly what must NOT happen.
assert_subcmd_version "atx power --version" atx power --version --url http://127.0.0.1:1
assert_subcmd_version "atx power -V" atx power -V --url http://127.0.0.1:1
assert_subcmd_version "info --version" info --version --url http://127.0.0.1:1
# A command with a required positional argument must still short-circuit.
assert_subcmd_version "msd remove --version (required arg)" msd remove --version --url http://127.0.0.1:1
assert_subcmd_version "system set-hostname --version" system set-hostname --version --url http://127.0.0.1:1

section "--dry-run never touches the device"
# Write commands from many groups, all pointed at an unreachable URL: with
# --dry-run they must print a preview and exit 0 without connecting.
assert_dry_run "atx power --dry-run" atx power --dry-run --url http://127.0.0.1:1
assert_dry_run "atx reset --dry-run" atx reset --dry-run --url http://127.0.0.1:1
assert_dry_run "msd remove --dry-run" msd remove smoke-test.iso --dry-run --url http://127.0.0.1:1
assert_dry_run "msd connect --dry-run" msd connect /dev/disk/by-uuid/X --dry-run --url http://127.0.0.1:1
assert_dry_run "wol add --dry-run" wol add AA:BB:CC:DD:EE:FF --dry-run --url http://127.0.0.1:1
assert_dry_run "wol wake --dry-run" wol wake AA:BB:CC:DD:EE:FF --dry-run --url http://127.0.0.1:1
assert_dry_run "screen set-mode --dry-run" screen set-mode clock_only --dry-run --url http://127.0.0.1:1
assert_dry_run "system set-hostname --dry-run" system set-hostname glkvm --dry-run --url http://127.0.0.1:1
assert_dry_run "tailscale stop --dry-run" tailscale stop --dry-run --url http://127.0.0.1:1
assert_dry_run "netbird start --dry-run" netbird start --dry-run --url http://127.0.0.1:1
assert_dry_run "zerotier set-token --dry-run" zerotier set-token 8056c2e21c000001 --dry-run --url http://127.0.0.1:1
assert_dry_run "repeater enable --dry-run" repeater enable true --dry-run --url http://127.0.0.1:1
assert_dry_run "ap close-all --dry-run" ap close-all --dry-run --url http://127.0.0.1:1
assert_dry_run "modem input-pin --dry-run" modem input-pin 1234 --dry-run --url http://127.0.0.1:1
assert_dry_run "upgrade reboot --dry-run" upgrade reboot --dry-run --url http://127.0.0.1:1
assert_dry_run "twofa delete --dry-run" twofa delete --dry-run --url http://127.0.0.1:1
assert_dry_run "fingerbot click --dry-run" fingerbot click --dry-run --url http://127.0.0.1:1
assert_dry_run "init run --dry-run" init run --dry-run --url http://127.0.0.1:1
assert_dry_run "config set --dry-run" config set url https://example.com --dry-run --url http://127.0.0.1:1
assert_dry_run "hid key --dry-run" hid key Enter --dry-run --url http://127.0.0.1:1
assert_dry_run "hid mouse click --dry-run" hid mouse click left --dry-run --url http://127.0.0.1:1
assert_dry_run "switch set-active --dry-run" switch set-active 1 --dry-run --url http://127.0.0.1:1
assert_dry_run "streamer set-params --dry-run" streamer set-params --quality 90 --dry-run --url http://127.0.0.1:1
assert_dry_run "asr start --dry-run" asr start --dry-run --url http://127.0.0.1:1
assert_dry_run "auth logout --dry-run" auth logout --dry-run --url http://127.0.0.1:1
# --dry-run combined with --yes/-f still previews and exits 0.
assert_dry_run "msd format --yes --dry-run" msd format --yes --dry-run --url http://127.0.0.1:1
assert_dry_run "atx reset -f --dry-run" atx reset -f --dry-run --url http://127.0.0.1:1

section "--format validation"
# An unknown --format is a usage error (exit 2), not a silent table fallback.
run --format bogus info
if [ "$RC" -eq 2 ]; then
  pass "--format bogus exits 2 (usage)"
else
  fail "--format bogus: expected exit 2, got $RC"
fi
if printf '%s' "$OUT" | grep -qi 'format'; then
  pass "--format bogus reports the invalid format"
else
  fail "--format bogus did not mention the format: $(printf '%s' "$OUT" | head -n1)"
fi

# ---------------------------------------------------------------------------
# Shell completion
# ---------------------------------------------------------------------------
section "Shell completion"
for shell in bash zsh fish powershell; do
  assert_ok_contains "completion $shell" 'kvm-cli' completion "$shell"
done

# ---------------------------------------------------------------------------
# Embedded skill
# ---------------------------------------------------------------------------
section "Embedded skill"
assert_ok_contains "skill print" 'name:' skill print
assert_ok_contains "skill path" 'kvm-cli' skill path
assert_ok "skill --help" skill --help

# ---------------------------------------------------------------------------
# Man pages ()
# ---------------------------------------------------------------------------
section "Man pages"
assert_ok "man (root)" man
assert_ok_contains "man prints roff" '.SH SYNOPSIS' man
assert_ok "man config" man config
# Nested subcommand paths must resolve (previously rejected by MaxNArgs(1)).
assert_ok "man hid key (nested)" man hid key
assert_ok "man msd format (nested)" man msd format
assert_ok_contains "man hid key prints SYNOPSIS" 'SYNOPSIS' man hid key
# Positional metavars must survive go-md2man: config get renders "get KEY".
assert_ok_contains "man config get preserves KEY" 'KEY' man config get

# ---------------------------------------------------------------------------
# Every command / subcommand --help
# ---------------------------------------------------------------------------
section "Command tree --help coverage"

# Recursively enumerate commands from Cobra's "Available Commands:" sections.
# Produces a newline-delimited list of space-separated command paths.
CMDS=""
enumerate() {
  local prefix="$1" out name child
  local -a pre
  if [ -n "$prefix" ]; then
    # Intentional word-splitting of the command path into argv (bash, not zsh).
    # shellcheck disable=SC2206
    pre=($prefix)
    out="$("$BIN" "${pre[@]}" --help 2>/dev/null)"
  else
    out="$("$BIN" --help 2>/dev/null)"
  fi
  while IFS= read -r name; do
    [ -z "$name" ] && continue
    # `help` is a built-in topic, not a real command in the tree.
    [ "$name" = "help" ] && continue
    child="$prefix${prefix:+ }$name"
    CMDS="${CMDS}${child}"$'\n'
    enumerate "$child"
  done < <(printf '%s\n' "$out" | awk '
    /^Available Commands:/ { f=1; next }
    f && /^[[:space:]]*$/  { f=0 }
    f                     { print $1 }
  ')
}
enumerate ""

TREE_TOTAL=0
while IFS= read -r path; do
  [ -z "$path" ] && continue
  # shellcheck disable=SC2206
  ARGS=($path)
  run "${ARGS[@]}" --help
  TREE_TOTAL=$((TREE_TOTAL + 1))
  if [ "$RC" -eq 0 ]; then
    pass "help: kvm-cli $path"
  else
    fail "help: kvm-cli $path (exit $RC)"
  fi
done <<< "$CMDS"

if [ "$TREE_TOTAL" -gt 0 ]; then
  pass "command tree has $TREE_TOTAL commands/subcommands (all --help OK)"
else
  fail "command tree enumeration found 0 commands"
fi

# ---------------------------------------------------------------------------
# Negative cases (usage errors)
# ---------------------------------------------------------------------------
section "Error handling"

# Unknown command: non-zero exit + "Did you mean" suggestion.
run screensot
if [ "$RC" -ne 0 ] && printf '%s' "$OUT" | grep -q 'Did you mean'; then
  pass "unknown command exits non-zero with suggestion"
else
  fail "unknown command: expected non-zero + 'Did you mean' (exit $RC)"
fi
if printf '%s' "$OUT" | grep -q 'screenshot'; then
  pass "unknown command suggests 'screenshot'"
else
  fail "unknown command did not suggest 'screenshot'"
fi

# Unknown flag: usage error, exit code 2.
run --definitely-not-a-flag
if [ "$RC" -eq 2 ]; then
  pass "unknown flag exits 2 (usage)"
else
  fail "unknown flag: expected exit 2, got $RC"
fi

# Bad subcommand flag should also be a usage error (2).
run info --definitely-not-a-flag
if [ "$RC" -eq 2 ]; then
  pass "unknown subcommand flag exits 2 (usage)"
else
  fail "unknown subcommand flag: expected exit 2, got $RC"
fi

# ---------------------------------------------------------------------------
# Isolated config
# ---------------------------------------------------------------------------
section "Config (isolated HOME)"
TMP_HOME="$(mktemp -d 2>/dev/null || echo "${TMPDIR:-/tmp}/kvm-smoke-$$")"
mkdir -p "$TMP_HOME" 2>/dev/null || true
OUT="$(HOME="$TMP_HOME" "$BIN" config list 2>&1)"
RC=$?
if [ "$RC" -eq 0 ]; then
  pass "config list with isolated HOME"
else
  fail "config list with isolated HOME (exit $RC): $(printf '%s' "$OUT" | head -n1)"
fi
# config list must not create a config file when none exists.
if [ ! -f "$TMP_HOME/.config/kvm-cli/config.json" ]; then
  pass "config list did not create a config file"
else
  fail "config list created a config file in isolated HOME"
fi
rm -rf "$TMP_HOME" 2>/dev/null || true

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
printf '\n=== Results: %d passed, %d failed ===\n' "$PASS" "$FAIL"
if [ "$FAIL" -ne 0 ]; then
  printf '\nFailed checks:\n%s' "$FAILED"
  exit 1
fi
printf 'All smoke tests passed.\n'
exit 0
