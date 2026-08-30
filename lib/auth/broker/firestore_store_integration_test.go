package broker

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gcfirestore "cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFirestoreCodeStoreIntegration(t *testing.T) {
	if os.Getenv("TISSUES_AUTH_FIRESTORE_INTEGRATION") != "1" {
		t.Skip("set TISSUES_AUTH_FIRESTORE_INTEGRATION=1 with an explicit test project and named database")
	}
	project := strings.TrimSpace(os.Getenv("TISSUES_FIRESTORE_TEST_PROJECT"))
	database := strings.TrimSpace(os.Getenv("TISSUES_FIRESTORE_TEST_DATABASE"))
	if project == "" || database == "" || database == "(default)" {
		t.Fatal("explicit test project and non-default named Firestore database are required")
	}

	ctx := context.Background()
	client, err := gcfirestore.NewClientWithDatabase(ctx, project, database)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	store, err := NewFirestoreCodeStore(client)
	if err != nil {
		t.Fatal(err)
	}
	tracked := make(map[string]struct{})
	track := func(rawCode string) string {
		tracked[rawCode] = struct{}{}
		return rawCode
	}
	t.Cleanup(func() {
		for rawCode := range tracked {
			if _, err := store.codeRef(rawCode).Delete(ctx); err != nil && status.Code(err) != codes.NotFound {
				t.Errorf("clean exact authorization-code document: %v", err)
			}
		}
	})
	newCode := func() string {
		rawCode, err := randomToken(32)
		if err != nil {
			t.Fatal(err)
		}
		return track(rawCode)
	}
	value := firestoreTestCode(time.Now().UTC().Add(time.Hour).Truncate(time.Second))

	t.Run("save consume replay", func(t *testing.T) {
		rawCode := newCode()
		if err := store.SaveCode(ctx, rawCode, value); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ConsumeCode(ctx, rawCode, value.ClientID, value.RedirectURI, value.Resource, testVerifier); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ConsumeCode(ctx, rawCode, value.ClientID, value.RedirectURI, value.Resource, testVerifier); !errors.Is(err, ErrCodeNotFound) {
			t.Fatalf("replay error = %v", err)
		}
	})

	t.Run("mismatches preserve code", func(t *testing.T) {
		rawCode := newCode()
		if err := store.SaveCode(ctx, rawCode, value); err != nil {
			t.Fatal(err)
		}
		attempts := []struct{ clientID, redirectURI, resource, verifier string }{
			{"other", value.RedirectURI, value.Resource, testVerifier},
			{value.ClientID, value.RedirectURI + "/wrong", value.Resource, testVerifier},
			{value.ClientID, value.RedirectURI, "https://wrong.example/mcp", testVerifier},
			{value.ClientID, value.RedirectURI, value.Resource, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		}
		for index, attempt := range attempts {
			if _, err := store.ConsumeCode(ctx, rawCode, attempt.clientID, attempt.redirectURI, attempt.resource, attempt.verifier); !errors.Is(err, ErrCodeMismatch) {
				t.Fatalf("mismatch %d error = %v", index, err)
			}
		}
		if _, err := store.ConsumeCode(ctx, rawCode, value.ClientID, value.RedirectURI, value.Resource, testVerifier); err != nil {
			t.Fatalf("correct consume after mismatches = %v", err)
		}
	})

	t.Run("create collision preserves original", func(t *testing.T) {
		rawCode := newCode()
		if err := store.SaveCode(ctx, rawCode, value); err != nil {
			t.Fatal(err)
		}
		replacement := value
		replacement.ClientID = "replacement"
		if err := store.SaveCode(ctx, rawCode, replacement); err == nil {
			t.Fatal("second create unexpectedly overwrote authorization code")
		}
		if _, err := store.ConsumeCode(ctx, rawCode, value.ClientID, value.RedirectURI, value.Resource, testVerifier); err != nil {
			t.Fatalf("original binding after collision = %v", err)
		}
	})

	t.Run("concurrent consume", func(t *testing.T) {
		rawCode := newCode()
		if err := store.SaveCode(ctx, rawCode, value); err != nil {
			t.Fatal(err)
		}
		const attempts = 4
		errs := make([]error, attempts)
		var wait sync.WaitGroup
		for index := range attempts {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, errs[index] = store.ConsumeCode(ctx, rawCode, value.ClientID, value.RedirectURI, value.Resource, testVerifier)
			}()
		}
		wait.Wait()
		succeeded := 0
		for _, err := range errs {
			if err == nil {
				succeeded++
			}
		}
		if succeeded != 1 {
			t.Fatalf("successful concurrent consumes = %d, want 1; errors=%v", succeeded, errs)
		}
	})

	// Semantic expiry is unit-qualified rather than raced against a possibly
	// installed asynchronous TTL policy in a live database.
}
