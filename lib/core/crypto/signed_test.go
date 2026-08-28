package crypto

import "testing"

func TestEncodeDecodeSignedRoundTrip(t *testing.T) {
	secret := []byte("super-secret")
	in := struct {
		Next string `json:"next"`
		Exp  int64  `json:"exp"`
	}{
		Next: "/authorize?client_id=tissues",
		Exp:  123,
	}

	token, err := EncodeSigned(in, secret)
	if err != nil {
		t.Fatalf("EncodeSigned() error = %v", err)
	}

	var out struct {
		Next string `json:"next"`
		Exp  int64  `json:"exp"`
	}
	if err := DecodeSigned(token, secret, &out); err != nil {
		t.Fatalf("DecodeSigned() error = %v", err)
	}
	if out != in {
		t.Fatalf("decoded payload mismatch: got %+v, want %+v", out, in)
	}
}

func TestDecodeSignedRejectsTampering(t *testing.T) {
	secret := []byte("super-secret")
	token, err := EncodeSigned(map[string]string{"next": "/authorize"}, secret)
	if err != nil {
		t.Fatalf("EncodeSigned() error = %v", err)
	}

	if len(token) < 2 {
		t.Fatalf("token too short for tamper test")
	}
	tampered := token[:len(token)-1] + "A"

	var out map[string]string
	if err := DecodeSigned(tampered, secret, &out); err == nil {
		t.Fatalf("expected DecodeSigned() to fail for tampered token")
	}
}
