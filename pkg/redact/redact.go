// Package redact masks secret-named parameters so their values never reach
// user-facing output or debug logs.
//
// Several device endpoints pass secrets as URL query parameters (for example
// /api/ap/enable?key=<wifi-passphrase>) or echo them back in a response. Those
// values must not be printed to stdout or written verbatim to --debug logs.
// This package centralises the list of secret parameter names and the masking
// logic shared by the CLI commands (package cmd) and the HTTP client
// (package api).
package redact

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
)

// Mask is the placeholder substituted for every secret value.
const Mask = "***"

// secretKeys lists the parameter names whose values are always masked. The
// comparison is case-insensitive and normalised (see IsSecretKey), so
// "keyboard" is not treated as a secret. This is the exact-match core of the
// rule; compound and camelCase names are matched by secretSubstrings and the
// compound-key rule in IsSecretKey.
var secretKeys = map[string]struct{}{
	"key":          {},
	"password":     {},
	"passwd":       {},
	"psk":          {},
	"secret":       {},
	"token":        {},
	"pin":          {},
	"apikey":       {},
	"api_key":      {},
	"credential":   {},
	"credentials":  {},
	"authkey":      {},
	"auth_key":     {},
	"passphrase":   {},
	"old_password": {},
	"new_password": {},
}

// secretSubstrings mark a key as secret when any word segment (or the whole
// lower-cased key) contains one of them. They are long enough that substring
// matches stay precise: "keyboard" and "keymaps" contain none of them, while
// "wifi_password", "otp_secret", "access_token" and "client_secret" all do.
var secretSubstrings = []string{
	"password", "passwd", "secret", "token", "passphrase",
	"credential", "authkey", "apikey",
}

// keyNormalizer folds the separators that can appear in device field names into
// underscores so exact matches are separator-insensitive ("api-key" ==
// "api_key"). Dots are included because flattened table keys are dotted
// ("config.ssl_key").
var keyNormalizer = strings.NewReplacer("-", "_", ".", "_", " ", "_", "/", "_", ":", "_")

// IsSecretKey reports whether name identifies a secret-bearing parameter or
// field. Matching is case-insensitive and separator-insensitive, and recognises
// compound names: a key is secret when the whole normalised name is an exact
// match, when any word segment contains a strong secret word, or when it has a
// "key" segment as part of a compound name ("ssl_key", "private_key").
//
// The rules deliberately do not treat "keyboard", "keymaps" or
// "keyboard_enabled" as secret: "key" is only matched as a whole segment, so
// words that merely begin with it stay public.
func IsSecretKey(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	if _, ok := secretKeys[keyNormalizer.Replace(strings.ToLower(trimmed))]; ok {
		return true
	}

	segs := secretSegments(trimmed)
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		for _, sub := range secretSubstrings {
			if strings.Contains(seg, sub) {
				return true
			}
		}
		// "pin" is matched as a whole segment only: this catches "pin",
		// "user_pin" and "pin_code" without masking unrelated words such as
		// "typing" or "spin".
		if seg == "pin" {
			return true
		}
	}
	// A lone "key" is handled by the exact match above; a "key" segment makes
	// the name secret only when combined with another segment.
	if len(segs) > 1 {
		for _, seg := range segs {
			if seg == "key" {
				return true
			}
		}
	}
	return false
}

// secretSegments splits a key into lower-cased word segments. Separators
// (underscore, hyphen, dot, space, slash, colon) and camelCase boundaries both
// start a new segment, so "wifi_password", "wifi-password" and "wifiPassword"
// all yield ["wifi", "password"].
func secretSegments(name string) []string {
	var out []string
	var cur []rune
	runes := []rune(name)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i, r := range runes {
		switch r {
		case '_', '-', '.', ' ', '/', ':':
			flush()
			continue
		}
		if isUpper(r) && len(cur) > 0 {
			prev := cur[len(cur)-1]
			switch {
			case isLower(prev) || isDigit(prev):
				flush()
			case isUpper(prev):
				// Split the final capital of an acronym run from a following
				// word: "APIKey" -> ["api", "key"].
				if i+1 < len(runes) && isLower(runes[i+1]) {
					flush()
				}
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// Value returns Mask when name is a secret key and value otherwise.
func Value(name, value string) string {
	if IsSecretKey(name) {
		return Mask
	}
	return value
}

// paramRe matches a name=value pair in a query string or URL. The leading
// separator is captured so it can be preserved. Values stop at the next
// separator, whitespace, quote or fragment marker, so a URL embedded in an
// error message is redacted without swallowing the rest of the line.
var paramRe = regexp.MustCompile(`(^|[?&])([^=&\s"'#?]+)=([^&\s"'#]*)`)

// userInfoRe matches the "scheme://user[:password]@" prefix of a URL. The
// password is not named in the query string, so paramRe cannot see it; masking
// the whole userinfo component keeps a URL such as
// "https://admin:s3cret@host" from leaking its embedded credentials.
var userInfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/?#@\s]+)@`)

// jsonKVRe matches a JSON "name": value pair. The key (group 2) is inspected
// with IsSecretKey so a malformed or truncated JSON body can still have its
// secret-named values masked before it is embedded in an error message.
var jsonKVRe = regexp.MustCompile(`("([A-Za-z0-9_.\-]+)"\s*:\s*)("[^"]*"|[^,}\]\s]+)`)

// Params redacts the values of secret-named parameters and embedded URL
// credentials anywhere in s. It accepts a bare query string ("a=1&key=x"), a
// full URL ("https://host/p?key=x"), or a message containing either (such as a
// wrapped transport error).
func Params(s string) string {
	if s == "" {
		return s
	}
	s = paramRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := paramRe.FindStringSubmatch(match)
		if len(sub) != 4 {
			return match
		}
		sep, name, value := sub[1], sub[2], sub[3]
		if value == "" || value == Mask || !IsSecretKey(name) {
			return match
		}
		return sep + name + "=" + Mask
	})
	return userInfoRe.ReplaceAllString(s, "${1}"+Mask+"@")
}

// String redacts secret material from a textual payload such as a device
// response body. It first tries to parse the text as JSON and mask every
// secret-named key via Any; when the payload is not (valid) JSON it masks
// JSON-style "name": value pairs best-effort and then falls back to
// query-string/URL parameter redaction. It never returns a string containing a
// value whose name names a secret.
func String(s string) string {
	if s == "" {
		return s
	}
	trimmed := strings.TrimSpace(s)
	if trimmed != "" {
		var v any
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			if b, err := json.Marshal(Any(v)); err == nil {
				return string(b)
			}
		}
	}
	s = jsonKVRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := jsonKVRe.FindStringSubmatch(match)
		if len(sub) != 4 || !IsSecretKey(sub[2]) {
			return match
		}
		return sub[1] + `"` + Mask + `"`
	})
	return Params(s)
}

// URL is an alias for Params, named for call sites that redact a request URL.
func URL(s string) string { return Params(s) }

// Map returns a copy of m with every value whose key names a secret replaced by
// Mask. Nested maps and slices are handled recursively, which keeps an echoed
// device response from leaking a secret back to the user. Values that cannot
// carry a secret — nil, booleans and the empty string — are left untouched, so
// metadata fields such as "token_present" stay readable.
func Map(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if IsSecretKey(k) {
			if shouldMask(v) {
				out[k] = Mask
			} else {
				out[k] = v
			}
			continue
		}
		out[k] = Any(v)
	}
	return out
}

// shouldMask reports whether a value looks like secret material worth masking.
// Nil, booleans and the empty string are never secrets; other scalars, maps and
// slices are masked when their key names a secret.
func shouldMask(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != "" && t != Mask
	case bool:
		return false
	default:
		return true
	}
}

// Error wraps err so its rendered message never contains a secret query value,
// while preserving the original error for errors.Is / errors.As via Unwrap.
// The standard library embeds the full request URL in transport errors, so a
// raw error must be re-rendered through Params before it reaches the user.
func Error(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{err: err}
}

// redactedError is the error returned by Error. Its message is the wrapped
// error's message with every secret-named parameter value masked.
type redactedError struct{ err error }

func (e *redactedError) Error() string { return Params(e.err.Error()) }

func (e *redactedError) Unwrap() error { return e.err }

// Any returns a deep copy of v with every secret-named map key masked. It
// traverses maps of any key/value type (including the api.RawMap named type),
// slices and arrays via reflection; structs, byte slices and scalars are
// returned unchanged. It is the shape-agnostic counterpart to Map and is used
// by the output renderer, which must handle arbitrary decoded JSON values.
func Any(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			val := iter.Value().Interface()
			if IsSecretKey(k) {
				if shouldMask(val) {
					out[k] = Mask
				} else {
					out[k] = val
				}
				continue
			}
			out[k] = Any(val)
		}
		return out
	case reflect.Slice, reflect.Array:
		// Leave []byte (a binary payload) untouched.
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return v
		}
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = Any(rv.Index(i).Interface())
		}
		return out
	default:
		return v
	}
}
