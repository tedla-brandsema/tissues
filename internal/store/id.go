package store

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// IDLen is the fixed length of a tissues ID: 128 random bits, base32-encoded
// without padding, lowercased.
const IDLen = 26

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID returns a fresh random identifier. IDs carry no meaning: they do not
// encode creation time and are never used for chronological ordering.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("tissues: crypto/rand failed: " + err.Error())
	}
	return strings.ToLower(idEncoding.EncodeToString(b[:]))
}

// ValidID reports whether s is a well-formed tissues ID.
func ValidID(s string) bool {
	if len(s) != IDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
