package server

import "testing"

func TestVerifiedSecretsMatchExactPair(t *testing.T) {
	var v verifiedSecrets
	v.remember("H1", "door")
	if !v.known("H1", "door") {
		t.Fatal("a remembered pair was not known")
	}
	if v.known("H1", "wrong") || v.known("H2", "door") || v.known("H1door", "") || v.known("", "H1door") {
		t.Fatal("a different hash or secret matched a remembered pair")
	}
}
