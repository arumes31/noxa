package tlscert

import "crypto/tls"

// CurvePreferences is the key-exchange order for every noxa TLS endpoint.
// X25519MLKEM768 (hybrid post-quantum) comes first, so a TLS 1.3 peer that
// supports it negotiates a session that stays confidential against a future
// quantum adversary recording traffic today. Classical groups remain for
// peers without ML-KEM. Pinning the list keeps the hybrid group on even if
// the process environment sets GODEBUG=tlsmlkem=0.
func CurvePreferences() []tls.CurveID {
	return []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256}
}
