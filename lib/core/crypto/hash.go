package crypto

import (
	"crypto"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
)

// Hashing
// https://security.stackexchange.com/questions/39849/does-bcrypt-have-a-maximum-password-length
// https://blogs.dropbox.com/tech/2016/09/how-dropbox-securely-stores-your-passwords/

func Hmac(p []byte, t crypto.Hash, key []byte) ([]byte, error) {
	hf, err := hashFactory(t)
	if err != nil {
		return nil, err
	}

	h := hmac.New(hf, key)
	return calcHashSum(p, h)
}

func HmacEqual(mac1, mac2 []byte) bool {
	return hmac.Equal(mac1, mac2)
}

func Hash(p []byte, t crypto.Hash) ([]byte, error) {

	h, err := hashFactory(t)
	if err != nil {
		return nil, err
	}
	return calcHashSum(p, h())
}

func calcHashSum(p []byte, h hash.Hash) ([]byte, error) {

	if len(p) <= 0 {
		return nil, fmt.Errorf("zero bytes")
	}

	_, err := h.Write(p)
	if err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func hashFactory(h crypto.Hash) (func() hash.Hash, error) {
	switch h {
	case crypto.SHA256:
		return sha256.New, nil
	case crypto.SHA512:
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("unknown hash %v", h)
	}
}
