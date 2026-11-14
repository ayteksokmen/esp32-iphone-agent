package pointer

import (
	"fmt"
	"strings"
)

// The board types as a US keyboard: it sends the key a US layout has each character on. iOS reads those keys with
// the phone's own hardware keyboard layout, which follows the keyboard language in use. On a phone set to Turkish
// Q the US "i" key types "ı" and "'" types "i" (2026-11-14), so text meant for that layout is first turned into the
// US characters on the keys that type it there.

// Keyboard layouts the board can type for.
const (
	KeyboardUS = "us"
	KeyboardTR = "tr" // Turkish Q
)

// turkishQ: for each character, the US character whose key types it on iOS's Turkish Q hardware layout. Measured
// on an iPhone 16 Pro by typing every key (2026-11-14). Letters other than i and digits are where US has them;
// " @ # $ [ ] { } \ | ` ~ are on no plain or shifted key there.
var turkishQ = map[rune]rune{
	'ı': 'i', 'i': '\'', 'I': 'I', 'İ': '"',
	'ğ': '[', 'Ğ': '{', 'ü': ']', 'Ü': '}', 'ş': ';', 'Ş': ':',
	'ö': ',', 'Ö': '<', 'ç': '.', 'Ç': '>',
	'.': '/', ':': '?', ',': '\\', ';': '|', '<': '`', '>': '~',
	'!': '!', '\'': '@', '^': '#', '+': '$', '%': '%', '&': '^', '/': '&',
	'(': '*', ')': '(', '=': ')', '*': '-', '?': '_', '-': '=', '_': '+',
}

// ForKeyboard turns text into the US characters that type it on the given layout, and lists the characters that
// layout has on no key the board can press (left out). For US, or no layout, text is returned as it is.
func ForKeyboard(text, layout string) (string, []rune) {
	if layout != KeyboardTR {
		return text, nil
	}
	var out strings.Builder
	var missing []rune
	for _, r := range text {
		switch {
		case r == ' ' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' && r != 'i' || r >= 'A' && r <= 'Z' && r != 'I':
			out.WriteRune(r)
		default:
			if k, ok := turkishQ[r]; ok {
				out.WriteRune(k)
				continue
			}
			if folded := Typeable(string(r)); folded != "" && folded != string(r) {
				k, _ := ForKeyboard(folded, layout) // é → e
				out.WriteString(k)
				continue
			}
			missing = append(missing, r)
		}
	}
	return out.String(), missing
}

// ValidKeyboard: layout is one ForKeyboard knows ("" is the US default).
func ValidKeyboard(layout string) error {
	switch layout {
	case "", KeyboardUS, KeyboardTR:
		return nil
	}
	return fmt.Errorf("keyboard %q not known (us, tr)", layout)
}
