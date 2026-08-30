package tissues

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"strings"
)

const IDLen = 26

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

type IDGenerator func() (string, error)

// TenantID is the stable Tissues-owned identity of a tenant. It deliberately
// uses the same random, opaque syntax as other Tissues IDs and carries no
// timestamp or provider semantics.
type TenantID string

func ParseTenantID(value string) (TenantID, error) {
	if !ValidID(value) {
		return "", fmt.Errorf("%w: malformed tenant ID", ErrInvalid)
	}
	return TenantID(value), nil
}

func (id TenantID) String() string { return string(id) }

func (id TenantID) Validate() error {
	if !ValidID(string(id)) {
		return fmt.Errorf("%w: malformed tenant ID", ErrInvalid)
	}
	return nil
}

func NewID() (string, error) { return newID(rand.Reader) }

func newID(source io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(source, raw[:]); err != nil {
		return "", fmt.Errorf("generate tissues ID: %w", err)
	}
	return strings.ToLower(idEncoding.EncodeToString(raw[:])), nil
}

func ValidID(id string) bool {
	if len(id) != IDLen {
		return false
	}
	for i := range len(id) {
		if c := id[i]; (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
