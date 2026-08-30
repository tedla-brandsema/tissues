package tissues

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIDContract(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != IDLen || !ValidID(id) {
			t.Fatalf("invalid ID %q", id)
		}
		if id != strings.ToLower(id) {
			t.Fatalf("ID not lowercase: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
	}
}
func TestIDEntropyFailure(t *testing.T) {
	if _, err := newID(errorReader{}); err == nil || !strings.Contains(err.Error(), "entropy") {
		t.Fatalf("newID error=%v", err)
	}
}

func TestTenantIDValidation(t *testing.T) {
	const valid = "7womw3jzkek74oggxj6f42xak4"
	id, err := ParseTenantID(valid)
	if err != nil || id.String() != valid || id.Validate() != nil {
		t.Fatalf("ParseTenantID(%q) = %q, %v", valid, id, err)
	}
	for _, invalid := range []string{"", "default", "7WOMW3JZKEK74OGGXJ6F42XAK4", "7womw3jzkek74oggxj6f42xak1", valid + "a"} {
		if _, err := ParseTenantID(invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseTenantID(%q) error = %v", invalid, err)
		}
	}
}

func TestCommentOrderingTiesBreakByID(t *testing.T) {
	now := time.Now().UTC()
	comments := []*Comment{{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbb", Created: now}, {ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Created: now}}
	SortComments(comments)
	if comments[0].ID != "aaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("comments=%#v", comments)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
