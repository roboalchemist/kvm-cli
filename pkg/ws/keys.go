package ws

import (
	"fmt"
	"sort"
	"strings"
)

// explicitKeys lists every non-generated DOM KeyboardEvent.code value accepted
// by the device. Generated codes (KeyA..KeyZ, Digit0..Digit9, F1..F24,
// Numpad0..Numpad9) are added programmatically in init.
var explicitKeys = []string{
	"Enter", "Escape", "Tab", "Space", "Backspace",
	"ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
	"ControlLeft", "ControlRight", "ShiftLeft", "ShiftRight",
	"AltLeft", "AltRight", "MetaLeft", "MetaRight",
	"Insert", "Home", "PageUp", "Delete", "End", "PageDown",
	"PrintScreen", "CapsLock", "NumLock", "ScrollLock", "Pause", "ContextMenu",
	"Minus", "Equal", "BracketLeft", "BracketRight", "Backslash",
	"Semicolon", "Quote", "Backquote", "Comma", "Period", "Slash",
	"IntlBackslash", "IntlRo", "IntlYen",
	"NumpadAdd", "NumpadSubtract", "NumpadMultiply", "NumpadDivide",
	"NumpadDecimal", "NumpadEnter", "NumpadEqual", "NumpadComma",
}

// validKeys is the set of accepted DOM KeyboardEvent.code names.
var validKeys map[string]struct{}

// keyAliases maps common human-friendly names to DOM codes.
var keyAliases = map[string]string{
	// modifiers
	"ctrl": "ControlLeft", "control": "ControlLeft",
	"lctrl": "ControlLeft", "rctrl": "ControlRight",
	"shift": "ShiftLeft", "lshift": "ShiftLeft", "rshift": "ShiftRight",
	"alt": "AltLeft", "lalt": "AltLeft", "ralt": "AltRight", "opt": "AltLeft", "option": "AltLeft",
	"meta": "MetaLeft", "win": "MetaLeft", "super": "MetaLeft", "cmd": "MetaLeft",
	"command": "MetaLeft", "lwin": "MetaLeft", "rwin": "MetaRight", "lmeta": "MetaLeft", "rmeta": "MetaRight",
	// whitespace / editing
	"enter": "Enter", "return": "Enter", "esc": "Escape", "escape": "Escape",
	"tab": "Tab", "space": "Space", "spacebar": "Space", "space key": "Space",
	"backspace": "Backspace", "bksp": "Backspace",
	"del": "Delete", "delete": "Delete", "ins": "Insert", "insert": "Insert",
	// navigation
	"home": "Home", "end": "End",
	"pgup": "PageUp", "pageup": "PageUp", "page up": "PageUp",
	"pgdn": "PageDown", "pagedown": "PageDown", "page down": "PageDown",
	"up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight",
	"arrowup": "ArrowUp", "arrowdown": "ArrowDown", "arrowleft": "ArrowLeft", "arrowright": "ArrowRight",
	// locks / misc
	"caps": "CapsLock", "capslock": "CapsLock", "caps lock": "CapsLock",
	"num": "NumLock", "numlock": "NumLock", "num lock": "NumLock",
	"scroll": "ScrollLock", "scrolllock": "ScrollLock", "scroll lock": "ScrollLock",
	"printscreen": "PrintScreen", "prtsc": "PrintScreen", "prtscr": "PrintScreen", "print screen": "PrintScreen",
	"pause": "Pause", "contextmenu": "ContextMenu", "menu": "ContextMenu",
	// punctuation
	"minus": "Minus", "equal": "Equal", "equals": "Equal",
	"bracketleft": "BracketLeft", "bracketright": "BracketRight",
	"backslash": "Backslash", "semicolon": "Semicolon", "quote": "Quote",
	"backquote": "Backquote", "grave": "Backquote",
	"comma": "Comma", "period": "Period", "dot": "Period", "slash": "Slash",
	// numpad
	"numpadadd": "NumpadAdd", "numpadsubtract": "NumpadSubtract",
	"numpadmultiply": "NumpadMultiply", "numpaddivide": "NumpadDivide",
	"numpaddecimal": "NumpadDecimal", "numpadenter": "NumpadEnter",
	"numpadequal": "NumpadEqual", "numpadcomma": "NumpadComma",
}

func init() {
	validKeys = make(map[string]struct{}, len(explicitKeys)+26+10+24+10)
	for _, k := range explicitKeys {
		validKeys[k] = struct{}{}
	}
	for c := 'A'; c <= 'Z'; c++ {
		validKeys[fmt.Sprintf("Key%c", c)] = struct{}{}
	}
	for d := '0'; d <= '9'; d++ {
		validKeys[fmt.Sprintf("Digit%c", d)] = struct{}{}
	}
	for i := 1; i <= 24; i++ {
		validKeys[fmt.Sprintf("F%d", i)] = struct{}{}
	}
	for i := 0; i <= 9; i++ {
		validKeys[fmt.Sprintf("Numpad%d", i)] = struct{}{}
	}
}

// IsValidKey reports whether name is an exact DOM KeyboardEvent.code accepted by
// the device. It does not accept aliases.
func IsValidKey(name string) bool {
	_, ok := validKeys[name]
	return ok
}

// ValidKeys returns every accepted exact key code, sorted.
func ValidKeys() []string {
	out := make([]string, 0, len(validKeys))
	for k := range validKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ResolveKey maps a user-supplied key name to an exact DOM KeyboardEvent.code.
// It accepts exact codes (e.g. "KeyA"), common aliases (e.g. "ctrl", "enter",
// "del"), bare letters ("a" -> "KeyA"), bare digits ("1" -> "Digit1") and
// function keys ("f5" -> "F5"). Unknown names yield a descriptive error.
func ResolveKey(name string) (string, error) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return "", fmt.Errorf("empty key name")
	}
	if IsValidKey(raw) {
		return raw, nil
	}

	lower := strings.ToLower(raw)
	if alias, ok := keyAliases[lower]; ok {
		return alias, nil
	}
	if IsValidKey(lower) {
		return lower, nil
	}

	// Bare single letter / digit.
	if len(raw) == 1 {
		switch {
		case raw[0] >= 'a' && raw[0] <= 'z':
			code := "Key" + strings.ToUpper(raw)
			if IsValidKey(code) {
				return code, nil
			}
		case raw[0] >= 'A' && raw[0] <= 'Z':
			code := "Key" + raw
			if IsValidKey(code) {
				return code, nil
			}
		case raw[0] >= '0' && raw[0] <= '9':
			code := "Digit" + raw
			if IsValidKey(code) {
				return code, nil
			}
		}
	}

	// Function keys f1..f24.
	if strings.HasPrefix(lower, "f") {
		if n, err := parseUint(lower[1:]); err == nil && n >= 1 && n <= 24 {
			return fmt.Sprintf("F%d", n), nil
		}
	}

	return "", fmt.Errorf("unknown key %q (use a DOM KeyboardEvent.code such as KeyA, Enter, ArrowUp, ControlLeft; run 'kvm-cli hid keys' for the full list)", name)
}

// ResolveCombo resolves an ordered slice of key names into exact DOM codes.
func ResolveCombo(keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("empty key combination")
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		code, err := ResolveKey(k)
		if err != nil {
			return nil, err
		}
		out = append(out, code)
	}
	return out, nil
}

// parseUint parses a small non-negative base-10 integer without importing strconv
// in the hot alias path (keeps the validation self-contained).
func parseUint(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
