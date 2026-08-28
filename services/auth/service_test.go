package auth

import "testing"

func TestInactiveAuthNeedsNoCredentials(t *testing.T) {
	if err := (Config{}).ValidateConfig(); err != nil {
		t.Fatal(err)
	}
}
func TestParseEntitlements(t *testing.T) {
	got := parseEntitlements("sub:one=tissues,other;email:a@example.test=tissues")
	if _, ok := got["sub:one"]["other"]; !ok {
		t.Fatalf("entitlements=%#v", got)
	}
}
