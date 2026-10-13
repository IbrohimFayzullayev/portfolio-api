package tgauth

import (
	"errors"
	"net/url"
	"strconv"
	"testing"
	"time"
)

const token = "123456:TEST-token"

func sample(now time.Time) url.Values {
	return url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {"AAHdF6IQAAAAAN0XohDhrOrc"},
		"user":      {`{"id":42,"first_name":"Ibrohim","username":"ibrohim"}`},
		// Present in current clients; covered by the hash like any other field.
		"signature": {"c2lnbmF0dXJl"},
	}
}

func TestVerify(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	raw := Sign(sample(now), token)

	got, err := Verify(raw, token, time.Hour, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.User.ID != 42 || got.User.Username != "ibrohim" {
		t.Errorf("user = %+v", got.User)
	}
}

// A known vector computed independently of this package (Python hmac), so
// the test does not merely check sign() against itself.
func TestVerifyKnownVector(t *testing.T) {
	raw := "auth_date=1791900000&query_id=AAHdF6IQAAAAAN0XohDhrOrc" +
		"&user=%7B%22id%22%3A42%2C%22first_name%22%3A%22Ibrohim%22%7D" +
		"&hash=" + knownHash
	now := time.Unix(1791900000, 0)
	if _, err := Verify(raw, token, time.Hour, now); err != nil {
		t.Fatalf("Verify(known vector): %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)

	tampered, _ := url.ParseQuery(Sign(sample(now), token))
	tampered.Set("user", `{"id":43,"first_name":"Someone"}`)

	noUser := sample(now)
	noUser.Del("user")

	tests := []struct {
		name  string
		raw   string
		token string
		at    time.Time
		want  error
	}{
		{"wrong bot token", Sign(sample(now), token), "999:other", now, ErrBadHash},
		{"edited after signing", tampered.Encode(), token, now, ErrBadHash},
		{"no hash", "auth_date=1&user=%7B%7D", token, now, ErrMissingHash},
		{"too old", Sign(sample(now), token), token, now.Add(2 * time.Hour), ErrExpired},
		{"from the future", Sign(sample(now.Add(10*time.Minute)), token), token, now, ErrExpired},
		{"signed but no user", Sign(noUser, token), token, now, ErrNoUser},
	}
	for _, tc := range tests {
		if _, err := Verify(tc.raw, tc.token, time.Hour, tc.at); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// hmac.new(hmac.new(b"WebAppData", token, sha256).digest(), dcs, sha256)
const knownHash = "b7e0a72a3cfc7ae7523dec1a84d0d95864c9c8fde2b50bc22910a0750a945b4b"
