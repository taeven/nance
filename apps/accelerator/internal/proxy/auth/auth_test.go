package auth

import "testing"

func TestParsePLAINPayload(t *testing.T) {
	// \0user\0pass
	p := []byte{0, 'u', 's', 'e', 'r', 0, 'p', 'a', 's', 's'}
	u, pw, err := ParsePLAINPayload(p)
	if err != nil || u != "user" || pw != "pass" {
		t.Fatalf("got %q %q err=%v", u, pw, err)
	}

	// user\0pass (no authzid)
	p2 := []byte{'d', 'e', 'm', 'o', 0, 't', 'o', 'k'}
	u, pw, err = ParsePLAINPayload(p2)
	if err != nil || u != "demo" || pw != "tok" {
		t.Fatalf("got %q %q err=%v", u, pw, err)
	}
}
