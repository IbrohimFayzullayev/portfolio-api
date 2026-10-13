// Package tgauth checks the init data a Telegram Mini App is opened with.
//
// When the bot's Web App button is pressed, Telegram hands the page a query
// string describing who opened it, signed with a key derived from the bot
// token. Only Telegram and this server know the token, so a string that
// verifies proves the page was opened from Telegram, by that user, recently.
// That replaces a login form: the Telegram account IS the credential, and
// the allow-list of user ids is the same one the bot already uses.
//
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
package tgauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrMissingHash = errors.New("init data has no hash")
	ErrBadHash     = errors.New("init data signature does not match")
	ErrExpired     = errors.New("init data is too old")
	ErrNoUser      = errors.New("init data has no user")
)

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type InitData struct {
	User     User
	AuthDate time.Time
}

// Verify checks the signature and age of raw init data and returns who it
// belongs to. maxAge bounds how long a captured string stays usable.
func Verify(raw, botToken string, maxAge time.Duration, now time.Time) (InitData, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return InitData{}, err
	}
	hash := values.Get("hash")
	if hash == "" {
		return InitData{}, ErrMissingHash
	}

	want := sign(values, botToken)
	got, err := hex.DecodeString(hash)
	if err != nil || !hmac.Equal(got, want) {
		return InitData{}, ErrBadHash
	}

	// Only now is anything in the string trusted.
	unix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return InitData{}, ErrExpired
	}
	authDate := time.Unix(unix, 0)
	if now.Sub(authDate) > maxAge || authDate.Sub(now) > time.Minute {
		return InitData{}, ErrExpired
	}

	var user User
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return InitData{}, ErrNoUser
	}
	return InitData{User: user, AuthDate: authDate}, nil
}

// sign computes the hash Telegram puts in "hash": HMAC-SHA256 of every other
// field as sorted "key=value" lines, keyed by HMAC-SHA256("WebAppData", token).
func sign(values url.Values, botToken string) []byte {
	keys := make([]string, 0, len(values))
	for k := range values {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + values.Get(k)
	}

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(botToken))

	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	return mac.Sum(nil)
}

// Sign builds init data the way Telegram would. For tests: nothing in the
// running server ever produces init data.
func Sign(values url.Values, botToken string) string {
	v := url.Values{}
	for k, vs := range values {
		v[k] = vs
	}
	v.Del("hash")
	v.Set("hash", hex.EncodeToString(sign(v, botToken)))
	return v.Encode()
}
