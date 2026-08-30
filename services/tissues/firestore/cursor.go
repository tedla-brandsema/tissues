package firestore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/tedla-brandsema/tissues/services/tissues"
)

const (
	cursorVersion     = 1
	projectCursorKind = "projects"
	issueCursorKind   = "issue-overviews"
)

type cursorPayload struct {
	Version    int    `json:"v"`
	TenantID   string `json:"tenant"`
	Kind       string `json:"query"`
	Filter     string `json:"filter,omitempty"`
	UpdatedNS  int64  `json:"updated_ns,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	Number     int64  `json:"number,omitempty"`
}

func encodeCursor(payload cursorPayload) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(tenantID tissues.TenantID, kind, filter, value string) (cursorPayload, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorPayload{}, invalidCursor()
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return cursorPayload{}, invalidCursor()
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return cursorPayload{}, invalidCursor()
	}
	if payload.Version != cursorVersion || payload.TenantID != tenantID.String() || payload.Kind != kind || payload.Filter != filter {
		return cursorPayload{}, invalidCursor()
	}
	switch kind {
	case projectCursorKind:
		canonical, err := tissues.CanonicalProjectKey(payload.ProjectKey)
		if err != nil || canonical != payload.ProjectKey || payload.Filter != "" || payload.UpdatedNS != 0 || payload.Number != 0 {
			return cursorPayload{}, invalidCursor()
		}
	case issueCursorKind:
		canonical, err := tissues.CanonicalProjectKey(payload.ProjectKey)
		if err != nil || canonical != payload.ProjectKey || payload.Number <= 0 {
			return cursorPayload{}, invalidCursor()
		}
		if payload.Filter != "" {
			filterKey, err := tissues.CanonicalProjectKey(payload.Filter)
			if err != nil || filterKey != payload.Filter || payload.ProjectKey != payload.Filter {
				return cursorPayload{}, invalidCursor()
			}
		}
	default:
		return cursorPayload{}, invalidCursor()
	}
	return payload, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	return fmt.Errorf("trailing JSON")
}

func invalidCursor() error { return fmt.Errorf("%w: invalid cursor", tissues.ErrInvalid) }
