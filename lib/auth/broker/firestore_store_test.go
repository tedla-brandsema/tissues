package broker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFirestoreCodeDocumentIDAndRawCodeSecrecy(t *testing.T) {
	rawCode := "fixed-authorization-code"
	want := "85a2c420d712c09a75d31a171eb4ab5fd15a71ec6fbbf621a3342564cb168c74"
	got := firestoreCodeDocumentID(rawCode)
	if got != want || len(got) != 64 || got != strings.ToLower(got) {
		t.Fatalf("document ID = %q, want %q", got, want)
	}
	if got == firestoreCodeDocumentID("different-authorization-code") {
		t.Fatal("different raw codes produced the same document ID")
	}
	path := firestoreAuthorizationCodesCollection + "/" + got
	if strings.Contains(path, rawCode) {
		t.Fatalf("document path contains raw authorization code: %q", path)
	}

	value := firestoreTestCode(time.Unix(2000, 0).UTC())
	entity, err := newFirestoreCodeEntity(value)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := entity
	corrupt.Subject = ""
	_, corruptErr := corrupt.authorizationCode()
	for label, text := range map[string]string{"document ID": got, "entity": fmt.Sprintf("%#v", entity), "error": fmt.Sprint(corruptErr)} {
		if strings.Contains(text, rawCode) {
			t.Fatalf("%s contains raw authorization code", label)
		}
	}
}

func TestFirestoreCodeEntityRoundTripAndDefensiveScopes(t *testing.T) {
	expiry := time.Unix(2000, 987654321).UTC()
	value := firestoreTestCode(expiry)
	entity, err := newFirestoreCodeEntity(value)
	if err != nil {
		t.Fatal(err)
	}
	if entity.Subject != value.Subject || entity.Email != value.Email || entity.ClientID != value.ClientID || entity.RedirectURI != value.RedirectURI || entity.Resource != value.Resource || entity.CodeChallenge != value.CodeChallenge || entity.CodeChallengeMethod != value.CodeChallengeMethod {
		t.Fatalf("entity = %#v", entity)
	}
	wantExpiry := time.Unix(expiry.Unix(), 0).UTC()
	if entity.ExpiresUnix != wantExpiry.Unix() || !entity.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expiry = %d / %s, want %d / %s", entity.ExpiresUnix, entity.ExpiresAt, wantExpiry.Unix(), wantExpiry)
	}
	value.Scopes[0] = "mutated input"
	if entity.Scopes[0] != "tissues:read" {
		t.Fatalf("encoded scopes share input backing array: %#v", entity.Scopes)
	}
	roundTrip, err := entity.authorizationCode()
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.Resource != entity.Resource || roundTrip.CodeChallenge != entity.CodeChallenge || roundTrip.CodeChallengeMethod != entity.CodeChallengeMethod || !roundTrip.ExpiresAt.Equal(wantExpiry) || !reflect.DeepEqual(roundTrip.Scopes, entity.Scopes) {
		t.Fatalf("round trip = %#v", roundTrip)
	}
	roundTrip.Scopes[0] = "mutated output"
	if entity.Scopes[0] != "tissues:read" {
		t.Fatalf("decoded scopes share entity backing array: %#v", entity.Scopes)
	}
}

func TestFirestoreCodeEntityRejectsCorruption(t *testing.T) {
	valid, err := newFirestoreCodeEntity(firestoreTestCode(time.Unix(2000, 0).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	for name, entity := range map[string]firestoreCodeEntity{
		"missing subject":        withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.Subject = "" }),
		"missing client":         withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.ClientID = "" }),
		"missing redirect":       withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.RedirectURI = "" }),
		"invalid expiry":         withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.ExpiresUnix = 0 }),
		"expiry disagreement":    withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.ExpiresAt = e.ExpiresAt.Add(time.Second) }),
		"missing PKCE method":    withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.CodeChallengeMethod = "" }),
		"missing PKCE challenge": withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.CodeChallenge = "" }),
		"invalid PKCE method":    withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.CodeChallengeMethod = "plain" }),
		"invalid PKCE challenge": withFirestoreCodeEntity(valid, func(e *firestoreCodeEntity) { e.CodeChallenge = "short" }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := entity.authorizationCode(); !errors.Is(err, errFirestoreCodeState) {
				t.Fatalf("error = %v, want corrupt state", err)
			}
		})
	}
}

func TestFirestoreCodeBindingFailuresDoNotAlterSemanticEntity(t *testing.T) {
	entity, err := newFirestoreCodeEntity(firestoreTestCode(time.Unix(2000, 0).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	for name, attempt := range map[string]struct {
		clientID, redirectURI, resource, verifier string
	}{
		"client":   {"other", entity.RedirectURI, entity.Resource, testVerifier},
		"redirect": {entity.ClientID, entity.RedirectURI + "/wrong", entity.Resource, testVerifier},
		"resource": {entity.ClientID, entity.RedirectURI, "https://wrong.example/mcp", testVerifier},
		"PKCE":     {entity.ClientID, entity.RedirectURI, entity.Resource, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := consumeFirestoreCodeEntity(entity, attempt.clientID, attempt.redirectURI, attempt.resource, attempt.verifier, time.Unix(1000, 0)); !errors.Is(err, ErrCodeMismatch) {
				t.Fatalf("error = %v, want mismatch", err)
			}
			if _, err := consumeFirestoreCodeEntity(entity, entity.ClientID, entity.RedirectURI, entity.Resource, testVerifier, time.Unix(1000, 0)); err != nil {
				t.Fatalf("correct attempt after mismatch = %v", err)
			}
		})
	}
	if _, err := consumeFirestoreCodeEntity(entity, entity.ClientID, entity.RedirectURI, entity.Resource, testVerifier, time.Unix(2001, 0)); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("expired error = %v, want ErrCodeExpired", err)
	}
}

func TestFirestoreCodeStoreRejectsUninitializedUse(t *testing.T) {
	if _, err := NewFirestoreCodeStore(nil); !errors.Is(err, errFirestoreCodeStore) {
		t.Fatalf("constructor error = %v", err)
	}
	var store *FirestoreCodeStore
	rawCode := "fixed-authorization-code"
	if err := store.SaveCode(context.Background(), rawCode, firestoreTestCode(time.Unix(2000, 0).UTC())); !errors.Is(err, errFirestoreCodeStore) || strings.Contains(err.Error(), rawCode) {
		t.Fatalf("SaveCode error = %v", err)
	}
	if _, err := store.ConsumeCode(context.Background(), rawCode, "client", "redirect", "resource", "verifier"); !errors.Is(err, errFirestoreCodeStore) || strings.Contains(err.Error(), rawCode) {
		t.Fatalf("ConsumeCode error = %v", err)
	}
}

func firestoreTestCode(expiry time.Time) authCode {
	return authCode{
		Subject: "subject", Email: "person@example.test", ClientID: "public-client",
		RedirectURI: "https://app.example.test/callback", Resource: "https://auth.example.test/mcp",
		Scopes: []string{"tissues:read", "tissues:write"}, CodeChallenge: testChallenge,
		CodeChallengeMethod: "S256", ExpiresAt: expiry,
	}
}

func withFirestoreCodeEntity(entity firestoreCodeEntity, change func(*firestoreCodeEntity)) firestoreCodeEntity {
	change(&entity)
	return entity
}
