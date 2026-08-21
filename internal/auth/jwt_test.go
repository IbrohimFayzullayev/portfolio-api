package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// A round-tripped token must parse back into the same subject and email.
func TestTokenManager_GenerateAndParse(t *testing.T) {
	tm := NewTokenManager("test-secret", time.Hour)
	userID := uuid.New()

	token, expiresAt, err := tm.Generate(userID, "ibrohim@example.com")
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if token == "" {
		t.Fatal("Generate returned an empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt %v should be in the future", expiresAt)
	}

	claims, err := tm.Parse(token)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if claims.Email != "ibrohim@example.com" {
		t.Errorf("email = %q, want %q", claims.Email, "ibrohim@example.com")
	}

	gotID, err := claims.UserID()
	if err != nil {
		t.Fatalf("UserID returned error: %v", err)
	}
	if gotID != userID {
		t.Errorf("UserID = %v, want %v", gotID, userID)
	}
}

// An expired token must be rejected by Parse.
func TestTokenManager_ExpiredToken(t *testing.T) {
	tm := NewTokenManager("test-secret", -time.Hour) // already expired
	token, _, err := tm.Generate(uuid.New(), "a@b.com")
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if _, err := tm.Parse(token); err == nil {
		t.Fatal("Parse accepted an expired token, want error")
	}
}

// A token signed with a different secret must be rejected.
func TestTokenManager_WrongSecret(t *testing.T) {
	issuer := NewTokenManager("secret-one", time.Hour)
	verifier := NewTokenManager("secret-two", time.Hour)

	token, _, err := issuer.Generate(uuid.New(), "a@b.com")
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	if _, err := verifier.Parse(token); err == nil {
		t.Fatal("Parse accepted a token signed with a different secret, want error")
	}
}

// Garbage input must not parse.
func TestTokenManager_MalformedToken(t *testing.T) {
	tm := NewTokenManager("test-secret", time.Hour)
	if _, err := tm.Parse("not-a-real-token"); err == nil {
		t.Fatal("Parse accepted a malformed token, want error")
	}
}
