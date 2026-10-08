// Package ort helps locate the ONNX Runtime shared library and choose
// execution providers for the local grounding backend. The library itself is
// never bundled: it is dlopen-ed at runtime (purego), so the kvm-cli binary
// stays CGO-free and the backend activates only when ONNX Runtime is installed
// (brew install onnxruntime, or the Linux distribution package).
package ort

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// LibraryName is the shared-library file name ONNX Runtime uses per platform.
func LibraryName() string { return libraryNameFor(runtime.GOOS) }

func libraryNameFor(goos string) string {
	switch goos {
	case "darwin":
		return "libonnxruntime.dylib"
	case "linux":
		return "libonnxruntime.so"
	default:
		return "onnxruntime.dll"
	}
}

// CommonLocations returns platform-specific candidate paths for the ONNX
// Runtime shared library, most-likely first.
func CommonLocations() []string { return commonLocationsFor(runtime.GOOS) }

// DiscoverLib resolves the ONNX Runtime shared library path: the
// KVM_ONNXRUNTIME_LIB environment variable wins, then the platform common
// locations (first existing file), then "" meaning let dlopen search system
// paths.
func DiscoverLib(envLib string) string {
	if s := strings.TrimSpace(envLib); s != "" {
		return s
	}
	for _, p := range CommonLocations() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func commonLocationsFor(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/opt/homebrew/lib/libonnxruntime.dylib",
			"/usr/local/lib/libonnxruntime.dylib",
		}
	case "linux":
		return []string{
			"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
			"/usr/lib/libonnxruntime.so",
			"/usr/local/lib/libonnxruntime.so",
		}
	default:
		return nil
	}
}

// ValidateMode checks an execution-provider mode string. Supported: auto, cpu,
// coreml, cuda (case-insensitive).
func ValidateMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "cpu", "coreml", "cuda", "":
		return nil
	default:
		return fmt.Errorf("unknown execution provider mode %q (want auto, cpu, coreml or cuda)", mode)
	}
}

// ExecutionProviders returns the ONNX execution-provider list for mode, in
// preference order. auto selects CoreML on macOS (Apple Silicon GPU/ANE) and
// CPU elsewhere; cpu forces CPU-only; coreml/cuda request that accelerator with
// CPU fallback. An empty mode behaves like auto.
func ExecutionProviders(mode string) []string {
	return executionProvidersFor(runtime.GOOS, mode)
}

func executionProvidersFor(goos, mode string) []string {
	cpu := "CPUExecutionProvider"
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "cpu":
		return []string{cpu}
	case "coreml":
		return []string{"CoreMLExecutionProvider", cpu}
	case "cuda":
		return []string{"CUDAExecutionProvider", cpu}
	default: // auto
		if goos == "darwin" {
			return []string{"CoreMLExecutionProvider", cpu}
		}
		return []string{cpu}
	}
}
