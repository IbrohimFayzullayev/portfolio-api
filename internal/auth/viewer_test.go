package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestViewerTokenRoundTrip(t *testing.T) {
	v := NewViewerTokens("secret-at-least-32-characters-long!!", time.Hour)
	tok, exp, err := v.Generate(42)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(exp) <= 0 {
		t.Error("expiry in the past")
	}
	claims, err := v.Parse(tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.TelegramID != 42 {
		t.Errorf("TelegramID = %d", claims.TelegramID)
	}
}

// The whole point of a separate key: neither token works in the other place.
func TestViewerAndAdminTokensDoNotCross(t *testing.T) {
	const secret = "secret-at-least-32-characters-long!!"
	admin := NewTokenManager(secret, time.Hour)
	viewer := NewViewerTokens(secret, time.Hour)

	viewerTok, _, err := viewer.Generate(42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Parse(viewerTok); err == nil {
		t.Error("admin manager accepted a viewer token")
	}

	adminTok, _, err := admin.Generate(uuid.New(), "a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := viewer.Parse(adminTok); err == nil {
		t.Error("viewer manager accepted an admin token")
	}
}

func TestViewerTokenExpires(t *testing.T) {
	v := NewViewerTokens("secret-at-least-32-characters-long!!", -time.Minute)
	tok, _, err := v.Generate(42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Parse(tok); err == nil {
		t.Error("expired viewer token accepted")
	}
}
