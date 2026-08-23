package store

import "testing"

func TestNewID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != IDLen {
			t.Fatalf("NewID() = %q, want length %d, got %d", id, IDLen, len(id))
		}
		if !ValidID(id) {
			t.Fatalf("NewID() = %q, not accepted by ValidID", id)
		}
		if seen[id] {
			t.Fatalf("NewID() returned duplicate %q after %d draws", id, i)
		}
		seen[id] = true
	}
}

func TestValidID(t *testing.T) {
	valid := []string{
		"abcdefghijklmnopqrstuvwxyz",
		"zyxwvutsrqponmlkjihgfedcba",
		"234567234567234567234567ab",
	}
	for _, s := range valid {
		if !ValidID(s) {
			t.Errorf("ValidID(%q) = false, want true", s)
		}
	}
	invalid := map[string]string{
		"empty":          "",
		"too short":      "abcdefghijklmnopqrstuvwxy",
		"too long":       "abcdefghijklmnopqrstuvwxyza",
		"uppercase":      "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"digit 0":        "abcdefghijklmnopqrstuvwxy0",
		"digit 1":        "abcdefghijklmnopqrstuvwxy1",
		"digit 8":        "abcdefghijklmnopqrstuvwxy8",
		"padding":        "abcdefghijklmnopqrstuvwx==",
		"dash":           "abcdefghijklmnopqrstuvwx-y",
		"path traversal": "..............................",
	}
	for name, s := range invalid {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true, want false (%s)", s, name)
		}
	}
}
