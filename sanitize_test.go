package pst

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"plain", "Installing updates", 0, "Installing updates"},
		{"controls", "a\x00b\nc\td\x7fe\u0085f", 0, "a b c d e f"},
		{"invalid UTF-8", "a\xffb", 0, "a b"},
		{"collapse and trim", "  a   \t\n b  ", 0, "a b"},
		{"unicode space", "a\u00a0\u2003b", 0, "a b"},
		{"bidi removed", "abc\u202edef\u2066g\u2069", 0, "abcdefg"},
		{"invisible removed", "a\u200bb\ufeffc\u200dd\u2060e\u061cf", 0, "abcdef"},
		{"invisible between spaces", "a \u200b b", 0, "a b"},
		{"truncate with ellipsis", "abcdefghij", 8, "abcde…"},
		{"truncate rune boundary", "ééééé", 7, "éé…"},
		{"truncate trims space", "abc defgh", 7, "abc…"},
		{"exact fit", "abcdefgh", 8, "abcdefgh"},
		{"tiny max", "abcdef", 2, "ab"},
		{"tiny max rune", "éé", 1, ""},
		{"ellipsis only", "abcdef", 3, "…"},
		{"empty", "", 10, ""},
		{"only controls", "\x01\x02", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeText(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("SanitizeText(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
			if tt.max > 0 && len(got) > tt.max {
				t.Errorf("len = %d > %d", len(got), tt.max)
			}
		})
	}
}

func TestSanitizeTextAlwaysValid(t *testing.T) {
	in := strings.Repeat("x\x00é\u202e\xff ", 1000)
	for _, max := range []int{MaxMsgBytes, MaxTitleBytes} {
		s := SanitizeText(in, max)
		if !utf8.ValidString(s) || len(s) > max {
			t.Fatalf("bad result for max %d", max)
		}
		if err := (Report{State: StateIdle, Msg: s, Title: SanitizeText(in, MaxTitleBytes)}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSanitizeSegment(t *testing.T) {
	tests := []struct{ in, want string }{
		{"build", "build"},
		{"My Build #3", "My-Build-3"},
		{"us-east", "us-east"},
		{"a/b", "a-b"},
		{"日本", ""},
		{"--x--", "x"},
		{"x.y+z_w", "x.y+z_w"},
		{"", ""},
		{strings.Repeat("a", 40), strings.Repeat("a", 32)},
		{strings.Repeat("a", 31) + " b", strings.Repeat("a", 31)},
		{"é1", "1"},
	}
	for _, tt := range tests {
		got := SanitizeSegment(tt.in)
		if got != tt.want {
			t.Errorf("SanitizeSegment(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if got != "" && !validSegment(got) {
			t.Errorf("SanitizeSegment(%q) = %q is not a valid segment", tt.in, got)
		}
	}
}

func TestDisarmText(t *testing.T) {
	in := "safe\u202egnp.exe\u200b\u2067x\u2069"
	if got := DisarmText(in); got != "safegnp.exex" {
		t.Errorf("DisarmText = %q", got)
	}
	if got := DisarmText("plain ✓"); got != "plain ✓" {
		t.Errorf("DisarmText changed plain text: %q", got)
	}
}
