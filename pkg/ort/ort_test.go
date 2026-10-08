package ort

import (
	"reflect"
	"testing"
)

func TestValidateMode(t *testing.T) {
	for _, ok := range []string{"", "auto", "cpu", "coreml", "cuda", " CUDA ", "Auto"} {
		if err := ValidateMode(ok); err != nil {
			t.Errorf("ValidateMode(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidateMode("tpu"); err == nil {
		t.Error("ValidateMode(tpu) should fail")
	}
}

func TestExecutionProviders(t *testing.T) {
	cases := []struct {
		mode string
		want []string
	}{
		{"cpu", []string{"CPUExecutionProvider"}},
		{"coreml", []string{"CoreMLExecutionProvider", "CPUExecutionProvider"}},
		{"cuda", []string{"CUDAExecutionProvider", "CPUExecutionProvider"}},
		{"", nil}, // auto: platform-dependent, checked below
	}
	for _, tc := range cases {
		got := ExecutionProviders(tc.mode)
		if tc.want != nil && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExecutionProviders(%q) = %v, want %v", tc.mode, got, tc.want)
		}
	}
	// auto ends with the CPU fallback on every platform.
	auto := ExecutionProviders("auto")
	if auto[len(auto)-1] != "CPUExecutionProvider" {
		t.Errorf("auto providers = %v, want CPU fallback last", auto)
	}
}

func TestCommonLocationsAndLibraryName(t *testing.T) {
	if LibraryName() == "" {
		t.Error("LibraryName should be non-empty")
	}
	if len(CommonLocations()) == 0 {
		t.Error("CommonLocations should be non-empty")
	}
	for _, p := range CommonLocations() {
		if !contains(p, LibraryName()) {
			t.Errorf("location %q does not reference %q", p, LibraryName())
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestPlatformBranches(t *testing.T) {
	// Exercise every GOOS branch directly.
	if got := libraryNameFor("darwin"); got != "libonnxruntime.dylib" {
		t.Errorf("darwin lib = %q", got)
	}
	if got := libraryNameFor("linux"); got != "libonnxruntime.so" {
		t.Errorf("linux lib = %q", got)
	}
	if got := libraryNameFor("windows"); got != "onnxruntime.dll" {
		t.Errorf("windows lib = %q", got)
	}
	if got := libraryNameFor("plan9"); got != "onnxruntime.dll" {
		t.Errorf("unknown lib = %q", got)
	}
	if locs := commonLocationsFor("darwin"); len(locs) != 2 {
		t.Errorf("darwin locations = %v", locs)
	}
	if locs := commonLocationsFor("linux"); len(locs) != 3 {
		t.Errorf("linux locations = %v", locs)
	}
	if locs := commonLocationsFor("plan9"); locs != nil {
		t.Errorf("unknown locations = %v, want nil", locs)
	}
	if got := executionProvidersFor("linux", "auto"); !reflect.DeepEqual(got, []string{"CPUExecutionProvider"}) {
		t.Errorf("linux auto = %v", got)
	}
	if got := executionProvidersFor("darwin", "auto"); !reflect.DeepEqual(got, []string{"CoreMLExecutionProvider", "CPUExecutionProvider"}) {
		t.Errorf("darwin auto = %v", got)
	}
	if got := executionProvidersFor("plan9", "auto"); len(got) != 1 {
		t.Errorf("unknown auto = %v", got)
	}
}
