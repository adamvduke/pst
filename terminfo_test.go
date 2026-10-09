package pst

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "terminfo", "70", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTerminfoParse(t *testing.T) {
	tests := []struct {
		file  string
		magic uint16
		pst   bool
		ext   bool // has an extended section
	}{
		{"pst-present", 0o432, true, true},
		{"pst-absent", 0o432, false, true},
		{"pst-cancelled", 0o432, false, true},
		{"pst-plain", 0o432, false, false},
		{"pst-only", 0o432, true, true},
		{"pst-present-32", 0o1036, true, true},
		{"pst-absent-32", 0o1036, false, true},
		{"pst-cancelled-32", 0o1036, false, true},
		{"pst-plain-32", 0o1036, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data := readFixture(t, tt.file)
			if got := uint16(data[0]) | uint16(data[1])<<8; got != tt.magic {
				t.Fatalf("fixture magic = %#o, want %#o", got, tt.magic)
			}
			caps, err := terminfoExtStrings(data)
			if err != nil {
				t.Fatal(err)
			}
			if (len(caps) > 0) != tt.ext {
				t.Errorf("extended strings = %v, want extended section %v", caps, tt.ext)
			}
			_, named := caps["Pst"]
			if tt.file == "pst-cancelled" || tt.file == "pst-cancelled-32" {
				if !named {
					t.Error("cancelled Pst should still be named in the table")
				}
			}
			if tt.ext && tt.file != "pst-only" {
				// Inherited from xterm-256color.
				if !caps["Ms"] || !caps["Se"] {
					t.Errorf("expected xterm-256color extended strings, got %v", caps)
				}
			}
			got, err := terminfoHasPst(data)
			if err != nil || got != tt.pst {
				t.Errorf("terminfoHasPst = %v, %v; want %v", got, err, tt.pst)
			}
		})
	}
}

func TestTerminfoMalformed(t *testing.T) {
	good := readFixture(t, "pst-present")
	for n := 0; n < len(good); n++ {
		if _, err := terminfoHasPst(good[:n]); err == nil && n < 12 {
			t.Errorf("truncated to %d bytes: no error", n)
		}
	}
	bad := append([]byte(nil), good...)
	bad[0] = 0
	if _, err := terminfoHasPst(bad); !errors.Is(err, errTerminfo) {
		t.Errorf("bad magic err = %v", err)
	}
	neg := append([]byte(nil), good...)
	neg[2], neg[3] = 0xff, 0xff // names size -1
	if _, err := terminfoHasPst(neg); !errors.Is(err, errTerminfo) {
		t.Errorf("negative count err = %v", err)
	}
	if _, err := terminfoHasPst(make([]byte, maxTerminfoBytes+1)); err == nil {
		t.Error("oversized input accepted")
	}
}

func TestTerminfoLookup(t *testing.T) {
	if !hasTerminfo {
		if ok, err := TerminfoHasPst("pst-present"); ok || err != nil {
			t.Errorf("platform without terminfo = %v, %v; want false, nil", ok, err)
		}
		return
	}
	hexDir, err := filepath.Abs(filepath.Join("testdata", "terminfo"))
	if err != nil {
		t.Fatal(err)
	}
	// A letter-layout copy, as Linux uses.
	letterDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(letterDir, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(letterDir, "p", "pst-letter"), readFixture(t, "pst-present"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TERMINFO", "")

	t.Run("TERMINFO hex layout", func(t *testing.T) {
		t.Setenv("TERMINFO", hexDir)
		t.Setenv("TERMINFO_DIRS", "")
		check(t, "pst-present", true)
		check(t, "pst-absent", false)
		check(t, "pst-cancelled", false)
		check(t, "pst-present-32", true)
	})
	t.Run("TERMINFO_DIRS letter layout", func(t *testing.T) {
		t.Setenv("TERMINFO_DIRS", "/nonexistent:"+letterDir+":"+hexDir)
		check(t, "pst-letter", true)
		check(t, "pst-only", true)
	})
	t.Run("HOME/.terminfo", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Symlink(letterDir, filepath.Join(home, ".terminfo")); err != nil {
			t.Skip(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("TERMINFO_DIRS", "")
		check(t, "pst-letter", true)
	})
	t.Run("TERM default", func(t *testing.T) {
		t.Setenv("TERMINFO", hexDir)
		t.Setenv("TERM", "pst-present")
		check(t, "", true)
	})
	t.Run("missing", func(t *testing.T) {
		t.Setenv("TERMINFO", hexDir)
		t.Setenv("TERMINFO_DIRS", "")
		if _, err := TerminfoHasPst("pst-no-such-entry"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want fs.ErrNotExist", err)
		}
	})
	t.Run("invalid names", func(t *testing.T) {
		for _, name := range []string{"../etc/passwd", ".", "..", "a/b", "a\x00b"} {
			if _, err := TerminfoHasPst(name); err == nil {
				t.Errorf("TerminfoHasPst(%q) succeeded", name)
			}
		}
		t.Setenv("TERM", "")
		if _, err := TerminfoHasPst(""); err == nil {
			t.Error("empty TERM succeeded")
		}
	})
}

func check(t *testing.T, term string, want bool) {
	t.Helper()
	got, err := TerminfoHasPst(term)
	if err != nil || got != want {
		t.Errorf("TerminfoHasPst(%q) = %v, %v; want %v", term, got, err, want)
	}
}
