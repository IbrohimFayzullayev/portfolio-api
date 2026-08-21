package auth

import "testing"

// A hash must verify against the right password and reject the wrong one.
func TestHashAndCheckPassword(t *testing.T) {
	const plain = "supersecret123"

	hash, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if hash == "" {
		t.Fatal("HashPassword returned an empty hash")
	}
	if hash == plain {
		t.Fatal("hash must not equal the plaintext password")
	}

	if !CheckPassword(hash, plain) {
		t.Error("CheckPassword returned false for the correct password")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Error("CheckPassword returned true for the wrong password")
	}
}

// bcrypt salts every hash, so two hashes of the same password must differ
// yet both must still verify.
func TestHashPassword_IsSalted(t *testing.T) {
	const plain = "samePassword!"

	h1, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	h2, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	if h1 == h2 {
		t.Error("two hashes of the same password should differ (missing salt?)")
	}
	if !CheckPassword(h1, plain) || !CheckPassword(h2, plain) {
		t.Error("both salted hashes should verify against the original password")
	}
}

// CheckPassword must return false (not panic) for a hash that isn't valid bcrypt.
func TestCheckPassword_InvalidHash(t *testing.T) {
	if CheckPassword("this-is-not-a-bcrypt-hash", "whatever") {
		t.Error("CheckPassword returned true for an invalid hash")
	}
}
