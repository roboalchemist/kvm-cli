package redact

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestIsSecretKey(t *testing.T) {
	secret := []string{
		"key", "KEY", " key ", "password", "passwd", "psk", "secret",
		"token", "pin", "apikey", "api_key", "api-key", "credential", "Credential",
		"credentials", "authkey", "auth-key", "passphrase",
		"old_password", "new_password", "OLD-PASSWORD",
	}
	for _, k := range secret {
		if !IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = false, want true", k)
		}
	}
	benign := []string{"", "ssid", "enable", "keyboard", "username", "apn", "public"}
	for _, k := range benign {
		if IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = true, want false", k)
		}
	}
}

// TestIsSecretKeyBroadened covers the N2 rules: compound and camelCase
// names that exact matching used to miss are secret, while words that merely
// contain letters like "key" stay public.
func TestIsSecretKeyBroadened(t *testing.T) {
	secret := []string{
		"private_key", "wifi_password", "access_token", "client_secret",
		"otp_secret", "setup_key", "ssl_key", "auth_key", "api_key",
		"new_password", "old_password", "apikey", "authkey",
		"wifiPassword", "WiFi-Password", "APIKey", "SSHKey", "sslKey",
		"config.ssl_key", "ssl key", "pin_code", "user_pin", "pin",
		"database_credential", "my_api_key", "secret-key",
	}
	for _, k := range secret {
		if !IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = false, want true", k)
		}
	}

	benign := []string{
		"", " ", "keyboard", "keymaps", "keyboard_enabled", "default_product_id",
		"keys", "hotkey", "monkey", "typing", "spin", "keynote",
		"ssid", "username", "enable", "apn", "public",
		"ssl_cert", "model", "version", "battery", "is_running",
		"product_id", "hostname", "ip_address", "mac_address", "dns_servers",
		"timezone_name", "keyboard_leds",
	}
	for _, k := range benign {
		if IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = true, want false", k)
		}
	}
}

// TestAny verifies the reflection-based redactor handles named map types,
// nested maps/slices, non-string values and binary payloads.
func TestAny(t *testing.T) {
	type rawMap map[string]any

	in := rawMap{
		"ssl_key":       "PRIVATEKEY",
		"keyboard":      "us",
		"nested":        map[string]string{"password": "p", "ssid": "keep"},
		"list":          []any{map[string]any{"authkey": "a"}, "plain"},
		"bytes":         []byte{1, 2, 3},
		"pin":           1234,
		"token_present": true,
		"empty_secret":  "",
		"nil_secret":    nil,
	}

	got, ok := Any(in).(map[string]any)
	if !ok {
		t.Fatalf("Any returned %T, want map[string]any", Any(in))
	}
	if got["ssl_key"] != Mask {
		t.Errorf("ssl_key = %v, want %q", got["ssl_key"], Mask)
	}
	if got["keyboard"] != "us" {
		t.Errorf("keyboard = %v, want us", got["keyboard"])
	}
	nested := got["nested"].(map[string]any)
	if nested["password"] != Mask || nested["ssid"] != "keep" {
		t.Errorf("nested = %v", nested)
	}
	list := got["list"].([]any)
	if list[0].(map[string]any)["authkey"] != Mask || list[1] != "plain" {
		t.Errorf("list = %v", list)
	}
	if !reflect.DeepEqual(got["bytes"], []byte{1, 2, 3}) {
		t.Errorf("bytes = %v, want []byte{1,2,3}", got["bytes"])
	}
	if got["pin"] != Mask {
		t.Errorf("pin = %v, want %q", got["pin"], Mask)
	}
	if got["token_present"] != true {
		t.Errorf("token_present = %v, want true (booleans are not secrets)", got["token_present"])
	}
	if got["empty_secret"] != "" {
		t.Errorf("empty_secret = %v, want empty (empty is not masked)", got["empty_secret"])
	}
	if got["nil_secret"] != nil {
		t.Errorf("nil_secret = %v, want nil", got["nil_secret"])
	}

	// A non-map is returned unchanged, and nil stays nil.
	if Any(7).(int) != 7 {
		t.Error("Any(7) changed a scalar")
	}
	if Any(nil) != nil {
		t.Error("Any(nil) should be nil")
	}
}

func TestValue(t *testing.T) {
	if got := Value("key", "SUPERSECRET"); got != Mask {
		t.Fatalf("Value(key) = %q, want %q", got, Mask)
	}
	if got := Value("ssid", "my-hotspot"); got != "my-hotspot" {
		t.Fatalf("Value(ssid) = %q, want my-hotspot", got)
	}
}

func TestParams(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		mustHave []string
		mustNot  []string
	}{
		{
			name:     "bare query string",
			in:       "enable=true&key=SUPERSECRET",
			mustHave: []string{"enable=true", "key=***"},
			mustNot:  []string{"SUPERSECRET"},
		},
		{
			name:     "leading secret param",
			in:       "key=SUPERSECRET&enable=true",
			mustHave: []string{"key=***", "enable=true"},
			mustNot:  []string{"SUPERSECRET"},
		},
		{
			name:     "full URL",
			in:       "https://host/api/ap/enable?ssid=my-hotspot&key=SUPERSECRET&enable=true",
			mustHave: []string{"ssid=my-hotspot", "key=***", "enable=true"},
			mustNot:  []string{"SUPERSECRET"},
		},
		{
			name:     "transport error message",
			in:       `Post "http://127.0.0.1:1/api/ap/enable?enable=true&key=SUPERSECRET": dial tcp 127.0.0.1:1: connect: connection refused`,
			mustHave: []string{"key=***", "connection refused"},
			mustNot:  []string{"SUPERSECRET"},
		},
		{
			name:     "pin and token",
			in:       "pin=1234&token=abc",
			mustHave: []string{"pin=***", "token=***"},
			mustNot:  []string{"1234", "abc"},
		},
		{
			name:     "percent-encoded value",
			in:       "password=s3cr3t%26more&user=admin",
			mustHave: []string{"password=***", "user=admin"},
			mustNot:  []string{"s3cr3t"},
		},
		{
			name:     "no secrets untouched",
			in:       "ssid=my-hotspot&enable=true",
			mustHave: []string{"ssid=my-hotspot", "enable=true"},
		},
		{
			name:     "already masked",
			in:       "key=***&enable=true",
			mustHave: []string{"key=***"},
		},
		{
			name:     "empty secret left alone",
			in:       "key=&enable=true",
			mustHave: []string{"key=", "enable=true"},
		},
		{
			name:     "old and new password",
			in:       `Post "http://127.0.0.1:1/api/init/change_password?user=admin&old_password=OLDSECRET&new_password=NEWSECRET": dial tcp 127.0.0.1:1: connect: connection refused`,
			mustHave: []string{"old_password=***", "new_password=***", "user=admin", "connection refused"},
			mustNot:  []string{"OLDSECRET", "NEWSECRET"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Params(tc.in)
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("Params(%q) = %q, missing %q", tc.in, got, want)
				}
			}
			for _, bad := range tc.mustNot {
				if strings.Contains(got, bad) {
					t.Errorf("Params(%q) = %q, leaked %q", tc.in, got, bad)
				}
			}
		})
	}
}

// TestParamsRedactsURLUserInfo covers the D8 sibling: URL credentials
// ("scheme://user:password@host") do not use the name=value form, so they need
// their own rule.
func TestParamsRedactsURLUserInfo(t *testing.T) {
	cases := []struct {
		in      string
		mustNot []string
		mustHas []string
	}{
		{
			in:      "https://admin:s3cr3t@glkvm.example.com/api",
			mustNot: []string{"s3cr3t", "admin"},
			mustHas: []string{"https://***@glkvm.example.com/api"},
		},
		{
			in:      `Post "https://user:pass@host/p?key=X": boom`,
			mustNot: []string{"user:pass"},
			mustHas: []string{"https://***@host/p"},
		},
		{
			in:      "https://glkvm.example.com/api",
			mustHas: []string{"https://glkvm.example.com/api"},
		},
	}
	for _, tc := range cases {
		got := Params(tc.in)
		for _, bad := range tc.mustNot {
			if strings.Contains(got, bad) {
				t.Errorf("Params(%q) = %q, leaked %q", tc.in, got, bad)
			}
		}
		for _, want := range tc.mustHas {
			if !strings.Contains(got, want) {
				t.Errorf("Params(%q) = %q, missing %q", tc.in, got, want)
			}
		}
	}
}

// TestStringRedactsJSONBody covers the D9 helper: a valid JSON body has its
// secret-named fields masked (with non-secret fields preserved), a malformed
// body still has JSON-style secret pairs masked, and a query-string body falls
// back to parameter redaction.
func TestStringRedactsJSONBody(t *testing.T) {
	valid := `{"success":true,"keyboard":"us","ssl_key":"SECRET_LEAK","password":"SECRET_LEAK","token":"SECRET_LEAK"}`
	got := String(valid)
	for _, bad := range []string{"SECRET_LEAK"} {
		if strings.Contains(got, bad) {
			t.Errorf("String(valid JSON) leaked %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, `"keyboard":"us"`) {
		t.Errorf("String(valid JSON) dropped a non-secret field: %s", got)
	}
	if strings.Count(got, Mask) != 3 {
		t.Errorf("String(valid JSON) masks = %d, want 3: %s", strings.Count(got, Mask), got)
	}

	malformed := `{"ssl_key": "SECRET_LEAK", "ssid": "keep"`
	got = String(malformed)
	if strings.Contains(got, "SECRET_LEAK") {
		t.Errorf("String(malformed JSON) leaked: %s", got)
	}
	if !strings.Contains(got, `"ssid"`) {
		t.Errorf("String(malformed JSON) dropped ssid: %s", got)
	}

	got = String("enable=true&key=SECRET_LEAK")
	if strings.Contains(got, "SECRET_LEAK") || !strings.Contains(got, "key=***") {
		t.Errorf("String(query string) = %q", got)
	}

	if String("") != "" {
		t.Error("String(\"\") should stay empty")
	}
}

func TestError(t *testing.T) {
	if Error(nil) != nil {
		t.Error("Error(nil) should be nil")
	}
	base := errors.New(`Get "http://host/p?key=SUPERSECRET": boom`)
	wrapped := Error(base)
	if strings.Contains(wrapped.Error(), "SUPERSECRET") {
		t.Fatalf("Error leaked the secret: %v", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "key=***") {
		t.Fatalf("Error did not mask the secret: %v", wrapped)
	}
	if !errors.Is(wrapped, base) {
		t.Fatalf("Error broke errors.Is: %v", wrapped)
	}
	if errors.Unwrap(wrapped) != base {
		t.Fatalf("Error broke Unwrap: %v", wrapped)
	}
}

func TestMap(t *testing.T) {
	in := map[string]any{
		"success": true,
		"key":     "SUPERSECRET",
		"nested": map[string]any{
			"token": "abc",
			"ssid":  "keep",
		},
		"list": []any{
			map[string]any{"pin": "1234", "name": "keep"},
			"plain",
		},
	}
	masked := Map(in)
	if masked["success"] != true {
		t.Errorf("success = %v, want true", masked["success"])
	}
	if masked["key"] != Mask {
		t.Errorf("key = %v, want %q", masked["key"], Mask)
	}
	nested := masked["nested"].(map[string]any)
	if nested["token"] != Mask || nested["ssid"] != "keep" {
		t.Errorf("nested = %v", nested)
	}
	list := masked["list"].([]any)
	first := list[0].(map[string]any)
	if first["pin"] != Mask || first["name"] != "keep" {
		t.Errorf("list[0] = %v", first)
	}
	if list[1] != "plain" {
		t.Errorf("list[1] = %v, want plain", list[1])
	}
	if Map(nil) != nil {
		t.Error("Map(nil) should be nil")
	}
}
