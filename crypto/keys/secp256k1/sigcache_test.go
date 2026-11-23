package secp256k1

import "testing"

func TestSigCacheNeverAcceptsInvalid(t *testing.T) {
	EnableSigCache(true)
	defer EnableSigCache(false)
	priv := GenPrivKey()
	pub := priv.PubKey().(*PubKey)
	msg := []byte("hello")
	sig, err := priv.Sign(msg)
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), sig...)
	bad[10] ^= 1
	if pub.VerifySignature(msg, bad) {
		t.Fatal("invalid signature accepted")
	}
	// caching a valid triple only affects that exact triple
	if PreverifySignature(pub, msg, bad) {
		t.Fatal("pre-verification accepted an invalid signature")
	}
	if !PreverifySignature(pub, msg, sig) {
		t.Fatal("pre-verification rejected a valid signature")
	}
	if pub.VerifySignature([]byte("other"), sig) {
		t.Fatal("cache hit for different message")
	}
	if pub.VerifySignature(msg, bad) {
		t.Fatal("cache hit for different signature")
	}
	if !pub.VerifySignature(msg, sig) { // cache hit (consumed)
		t.Fatal("valid signature rejected")
	}
	if !pub.VerifySignature(msg, sig) { // miss -> real verification
		t.Fatal("valid signature rejected after consume")
	}
}
