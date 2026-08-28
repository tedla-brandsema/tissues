package broker

import (
	"context"
	"errors"
	"time"

	gcds "cloud.google.com/go/datastore"
)

const (
	defaultCodeStoreKind = "auth_code"
)

var (
	ErrCodeNotFound = errors.New("authorization code not found")
	ErrCodeExpired  = errors.New("authorization code expired")
	ErrCodeMismatch = errors.New("authorization code mismatch")
)

type DatastoreCodeStore struct {
	client    *gcds.Client
	namespace string
	kind      string
}

type codeEntity struct {
	Subject     string `datastore:"subject"`
	Email       string `datastore:"email"`
	ClientID    string `datastore:"client_id"`
	RedirectURI string `datastore:"redirect_uri,noindex"`
	ExpiresUnix int64  `datastore:"expires_unix"`
}

func NewDatastoreCodeStore(client *gcds.Client, namespace, kind string) *DatastoreCodeStore {
	if kind == "" {
		kind = defaultCodeStoreKind
	}
	return &DatastoreCodeStore{
		client:    client,
		namespace: namespace,
		kind:      kind,
	}
}

func (s *DatastoreCodeStore) SaveCode(ctx context.Context, code string, val authCode) error {
	if s == nil || s.client == nil {
		return errors.New("datastore code store is not initialized")
	}
	ent := codeEntity{
		Subject:     val.Subject,
		Email:       val.Email,
		ClientID:    val.ClientID,
		RedirectURI: val.RedirectURI,
		ExpiresUnix: val.ExpiresAt.Unix(),
	}
	_, err := s.client.Put(ctx, s.key(code), &ent)
	return err
}

func (s *DatastoreCodeStore) ConsumeCode(ctx context.Context, code, clientID, redirectURI string) (authCode, error) {
	if s == nil || s.client == nil {
		return authCode{}, errors.New("datastore code store is not initialized")
	}

	key := s.key(code)
	var out authCode
	_, err := s.client.RunInTransaction(ctx, func(tx *gcds.Transaction) error {
		var ent codeEntity
		if err := tx.Get(key, &ent); err != nil {
			if errors.Is(err, gcds.ErrNoSuchEntity) {
				return ErrCodeNotFound
			}
			return err
		}

		_ = tx.Delete(key)

		if time.Now().Unix() > ent.ExpiresUnix {
			return ErrCodeExpired
		}
		if ent.ClientID != clientID || ent.RedirectURI != redirectURI {
			return ErrCodeMismatch
		}
		out = authCode{
			Subject:     ent.Subject,
			Email:       ent.Email,
			ClientID:    ent.ClientID,
			RedirectURI: ent.RedirectURI,
			ExpiresAt:   time.Unix(ent.ExpiresUnix, 0),
		}
		return nil
	})
	return out, err
}

func (s *DatastoreCodeStore) key(code string) *gcds.Key {
	key := gcds.NameKey(s.kind, code, nil)
	key.Namespace = s.namespace
	return key
}
