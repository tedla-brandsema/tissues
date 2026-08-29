package broker

import (
	"reflect"
	"testing"
	"time"
)

func TestCodeEntityPreservesResourceScopesAndLegacyAbsence(t *testing.T) {
	value := authCode{Subject: "subject", Email: "email@example.test", ClientID: "tissues", RedirectURI: "https://app.example/callback", Resource: "https://auth.example/mcp", Scopes: []string{"tissues:read", "tissues:write"}, ExpiresAt: time.Unix(1234, 0)}
	entity := newCodeEntity(value)
	if entity.Resource != value.Resource || !reflect.DeepEqual(entity.Scopes, value.Scopes) {
		t.Fatalf("entity = %#v", entity)
	}
	roundTrip := entity.authorizationCode()
	if roundTrip.Resource != value.Resource || !reflect.DeepEqual(roundTrip.Scopes, value.Scopes) {
		t.Fatalf("round trip = %#v", roundTrip)
	}
	legacy := codeEntity{Subject: "subject", ClientID: "tissues", RedirectURI: "https://app.example/callback", ExpiresUnix: 1234}
	if legacy.Resource != "" || len(legacy.Scopes) != 0 {
		t.Fatalf("legacy entity = %#v", legacy)
	}
	if got, err := consumeCodeEntity(entity, value.ClientID, value.RedirectURI, value.Resource, time.Unix(1000, 0)); err != nil || got.Resource != value.Resource {
		t.Fatalf("resource-aware entity consume = %#v, %v", got, err)
	}
	for name, resource := range map[string]string{"missing": "", "wrong": "https://wrong.example/mcp"} {
		if _, err := consumeCodeEntity(entity, value.ClientID, value.RedirectURI, resource, time.Unix(1000, 0)); err != ErrCodeMismatch {
			t.Fatalf("%s resource error = %v, want %v", name, err, ErrCodeMismatch)
		}
	}
	for name, tc := range map[string]struct {
		clientID    string
		redirectURI string
	}{
		"client":   {"other", value.RedirectURI},
		"redirect": {value.ClientID, value.RedirectURI + "/wrong"},
	} {
		if _, err := consumeCodeEntity(entity, tc.clientID, tc.redirectURI, value.Resource, time.Unix(1000, 0)); err != ErrCodeMismatch {
			t.Fatalf("%s mismatch error = %v, want %v", name, err, ErrCodeMismatch)
		}
	}
	if got, err := consumeCodeEntity(legacy, "tissues", "https://app.example/callback", "", time.Unix(1000, 0)); err != nil || got.Resource != "" {
		t.Fatalf("legacy consume = %#v, %v", got, err)
	}
	if _, err := consumeCodeEntity(legacy, "tissues", "https://app.example/callback", value.Resource, time.Unix(1000, 0)); err != ErrCodeMismatch {
		t.Fatalf("legacy resource mismatch error = %v", err)
	}
	if _, err := consumeCodeEntity(entity, value.ClientID, value.RedirectURI, value.Resource, time.Unix(2000, 0)); err != ErrCodeExpired {
		t.Fatalf("expired error = %v, want %v", err, ErrCodeExpired)
	}
}
