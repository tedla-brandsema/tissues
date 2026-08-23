package model

import (
	"testing"
	"time"
)

// Timestamp is the seam through which callers turn a wall clock into a
// domain timestamp. Validate rejects anything else.
func TestTimestamp(t *testing.T) {
	in := time.Date(2026, 8, 23, 15, 20, 11, 987654321, time.FixedZone("CEST", 2*60*60))
	got := Timestamp(in)
	if want := "2026-08-23T13:20:11.987654321Z"; got.Format(time.RFC3339Nano) != want {
		t.Errorf("Timestamp(%v) = %v, want %s", in, got, want)
	}
	if got.Nanosecond() != 987654321 {
		t.Errorf("Timestamp discarded sub-second precision: %d ns", got.Nanosecond())
	}
	if got.Location() != time.UTC {
		t.Errorf("Timestamp location = %v, want UTC", got.Location())
	}
	if !Timestamp(got).Equal(got) {
		t.Error("Timestamp is not idempotent")
	}

	c := &Comment{ID: "x", Author: "a@b", Created: got, Updated: got, Body: "body"}
	if err := c.Validate(); err != nil {
		t.Errorf("normalized sub-second timestamps rejected: %v", err)
	}
	// Whole seconds remain perfectly valid.
	whole := Timestamp(time.Date(2026, 8, 23, 13, 20, 11, 0, time.UTC))
	c.Created, c.Updated = whole, whole
	if err := c.Validate(); err != nil {
		t.Errorf("whole-second timestamps rejected: %v", err)
	}
	// A non-UTC location is still refused.
	c.Created, c.Updated = in, in
	if err := c.Validate(); err == nil {
		t.Error("Validate accepted a non-UTC timestamp")
	}
}

func TestSortCommentsIsTotal(t *testing.T) {
	at := func(s string) time.Time {
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return ts.UTC()
	}
	// Same Created, so only the ID can order these, and it must do so the
	// same way every time regardless of input order.
	early, late := at("2026-08-23T09:00:00Z"), at("2026-08-23T10:00:00Z")
	a := &Comment{ID: "aaa", Created: late, Updated: late}
	b := &Comment{ID: "bbb", Created: late, Updated: early}
	c := &Comment{ID: "ccc", Created: early, Updated: late}
	for _, in := range [][]*Comment{{a, b, c}, {c, b, a}, {b, a, c}} {
		SortComments(in)
		if in[0] != c || in[1] != a || in[2] != b {
			t.Errorf("SortComments = %s %s %s, want ccc aaa bbb", in[0].ID, in[1].ID, in[2].ID)
		}
	}
}
