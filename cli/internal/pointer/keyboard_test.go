package pointer

import "testing"

// On a phone set to Turkish Q, Turkish text goes out as the US characters on the keys that type it there
// (measured 2026-11-14: the US "i" key types ı, "'" types i, "[" ğ, "/" a full stop ...).
func TestForKeyboardTurkishQ(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Yarın saat onda doktora gitmem gerekiyor", "Yarin saat onda doktora g'tmem gerek'yor"},
		{"ğüşöç ĞÜŞÖÇ İI ıi", "[];,. {}:<> \"I i'"},
		{"Saat 10:30, ne? Evet! (1+1=2) a-b_c/d. %5 & 'x' <y> *z; ^", "Saat 10?30\\ ne_ Evet! *1$1)2( a=b+c&d/ %5 ^ @x@ `y~ -z| #"},
		{"café", "cafe"},
	} {
		if got, missing := ForKeyboard(c.in, KeyboardTR); got != c.want || len(missing) > 0 {
			t.Errorf("%q: %q (left out %q), want %q", c.in, got, string(missing), c.want)
		}
	}
	if got, missing := ForKeyboard("a@b.com", KeyboardTR); got != "ab/com" || string(missing) != "@" {
		t.Errorf("@: %q, left out %q", got, string(missing))
	}
	if got, _ := ForKeyboard("gitmem ı", KeyboardUS); got != "gitmem ı" {
		t.Errorf("US changed the text: %q", got)
	}
	if ValidKeyboard("tr") != nil || ValidKeyboard("") != nil || ValidKeyboard("de") == nil {
		t.Error("ValidKeyboard")
	}
}
