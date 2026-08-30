package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	gcfirestore "cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const firestoreAuthorizationCodesCollection = "oauthAuthorizationCodes"

var (
	errFirestoreCodeStore = errors.New("Firestore authorization-code store failure")
	errFirestoreCodeState = errors.New("corrupt Firestore authorization-code state")
)

// FirestoreCodeStore persists global OAuth issuer state in Firestore Native.
// Client creation and database selection remain application responsibilities.
type FirestoreCodeStore struct {
	client *gcfirestore.Client
}

type firestoreCodeEntity struct {
	Subject             string    `firestore:"subject"`
	Email               string    `firestore:"email"`
	ClientID            string    `firestore:"client_id"`
	RedirectURI         string    `firestore:"redirect_uri"`
	Resource            string    `firestore:"resource"`
	Scopes              []string  `firestore:"scopes"`
	CodeChallenge       string    `firestore:"code_challenge"`
	CodeChallengeMethod string    `firestore:"code_challenge_method"`
	ExpiresUnix         int64     `firestore:"expires_unix"`
	ExpiresAt           time.Time `firestore:"expires_at"`
}

// NewFirestoreCodeStore constructs a CodeStore around an already-selected
// Firestore client. The store never creates or owns the client.
func NewFirestoreCodeStore(client *gcfirestore.Client) (*FirestoreCodeStore, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: client is required", errFirestoreCodeStore)
	}
	return &FirestoreCodeStore{client: client}, nil
}

func (s *FirestoreCodeStore) SaveCode(ctx context.Context, rawCode string, value authCode) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("%w: store is not initialized", errFirestoreCodeStore)
	}
	entity, err := newFirestoreCodeEntity(value)
	if err != nil {
		return err
	}
	if _, err := s.codeRef(rawCode).Create(ctx, entity); err != nil {
		return fmt.Errorf("%w: create failed", errFirestoreCodeStore)
	}
	return nil
}

func (s *FirestoreCodeStore) ConsumeCode(ctx context.Context, rawCode, clientID, redirectURI, resource, codeVerifier string) (authCode, error) {
	if s == nil || s.client == nil {
		return authCode{}, fmt.Errorf("%w: store is not initialized", errFirestoreCodeStore)
	}
	ref := s.codeRef(rawCode)
	var consumed authCode
	err := s.client.RunTransaction(ctx, func(_ context.Context, tx *gcfirestore.Transaction) error {
		doc, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return ErrCodeNotFound
		}
		if err != nil {
			return err
		}
		var entity firestoreCodeEntity
		if err := doc.DataTo(&entity); err != nil {
			return errFirestoreCodeState
		}
		code, err := consumeFirestoreCodeEntity(entity, clientID, redirectURI, resource, codeVerifier, time.Now())
		if err != nil {
			return err
		}
		if err := tx.Delete(ref); err != nil {
			return err
		}
		consumed = code
		return nil
	})
	if err == nil {
		return consumed, nil
	}
	if errors.Is(err, ErrCodeNotFound) || errors.Is(err, ErrCodeExpired) || errors.Is(err, ErrCodeMismatch) || errors.Is(err, errFirestoreCodeState) {
		return authCode{}, err
	}
	return authCode{}, fmt.Errorf("%w: consume failed", errFirestoreCodeStore)
}

func (s *FirestoreCodeStore) codeRef(rawCode string) *gcfirestore.DocumentRef {
	return s.client.Collection(firestoreAuthorizationCodesCollection).Doc(firestoreCodeDocumentID(rawCode))
}

func firestoreCodeDocumentID(rawCode string) string {
	digest := sha256.Sum256([]byte(rawCode))
	return hex.EncodeToString(digest[:])
}

func newFirestoreCodeEntity(value authCode) (firestoreCodeEntity, error) {
	expiresUnix := value.ExpiresAt.Unix()
	entity := firestoreCodeEntity{
		Subject:             value.Subject,
		Email:               value.Email,
		ClientID:            value.ClientID,
		RedirectURI:         value.RedirectURI,
		Resource:            value.Resource,
		Scopes:              append([]string(nil), value.Scopes...),
		CodeChallenge:       value.CodeChallenge,
		CodeChallengeMethod: value.CodeChallengeMethod,
		ExpiresUnix:         expiresUnix,
		ExpiresAt:           time.Unix(expiresUnix, 0).UTC(),
	}
	if _, err := entity.authorizationCode(); err != nil {
		return firestoreCodeEntity{}, err
	}
	return entity, nil
}

func consumeFirestoreCodeEntity(entity firestoreCodeEntity, clientID, redirectURI, resource, codeVerifier string, now time.Time) (authCode, error) {
	code, err := entity.authorizationCode()
	if err != nil {
		return authCode{}, err
	}
	if err := validateCodeBinding(code, clientID, redirectURI, resource, codeVerifier, now); err != nil {
		return authCode{}, err
	}
	return code, nil
}

func (entity firestoreCodeEntity) authorizationCode() (authCode, error) {
	expiresAt := time.Unix(entity.ExpiresUnix, 0).UTC()
	if entity.ExpiresUnix <= 0 || entity.ExpiresAt.IsZero() || !entity.ExpiresAt.Equal(expiresAt) {
		return authCode{}, fmt.Errorf("%w: invalid expiry", errFirestoreCodeState)
	}
	if strings.TrimSpace(entity.Subject) == "" || strings.TrimSpace(entity.ClientID) == "" || strings.TrimSpace(entity.RedirectURI) == "" {
		return authCode{}, fmt.Errorf("%w: missing required binding", errFirestoreCodeState)
	}
	if entity.CodeChallenge == "" || entity.CodeChallengeMethod == "" {
		if entity.CodeChallenge != "" || entity.CodeChallengeMethod != "" {
			return authCode{}, fmt.Errorf("%w: inconsistent PKCE binding", errFirestoreCodeState)
		}
	} else if entity.CodeChallengeMethod != "S256" || !validPKCEValue(entity.CodeChallenge) {
		return authCode{}, fmt.Errorf("%w: invalid PKCE binding", errFirestoreCodeState)
	}
	return authCode{
		Subject:             entity.Subject,
		Email:               entity.Email,
		ClientID:            entity.ClientID,
		RedirectURI:         entity.RedirectURI,
		Resource:            entity.Resource,
		Scopes:              append([]string(nil), entity.Scopes...),
		CodeChallenge:       entity.CodeChallenge,
		CodeChallengeMethod: entity.CodeChallengeMethod,
		ExpiresAt:           expiresAt,
	}, nil
}

var _ CodeStore = (*FirestoreCodeStore)(nil)
