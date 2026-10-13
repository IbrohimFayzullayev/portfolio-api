package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Viewer tokens: read-only access for the Telegram Mini App.
//
// The dashboard's admin token and this one must never be mistaken for each
// other, so they do not share a key. The viewer key is derived from
// JWT_SECRET (no new secret to manage) but is a different key: an admin
// token fails a viewer check and a viewer token fails an admin check at the
// signature, before any claim is even read. The audience claim is a second,
// independent label on top of that.

const viewerAudience = "telegram-webapp"

type ViewerClaims struct {
	jwt.RegisteredClaims
	TelegramID int64 `json:"tg_id"`
}

type ViewerTokens struct {
	secret []byte
	ttl    time.Duration
}

func NewViewerTokens(jwtSecret string, ttl time.Duration) *ViewerTokens {
	mac := hmac.New(sha256.New, []byte(jwtSecret))
	mac.Write([]byte("portfolio/telegram-webapp-viewer/v1"))
	return &ViewerTokens{secret: mac.Sum(nil), ttl: ttl}
}

func (v *ViewerTokens) Generate(telegramID int64) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(v.ttl)
	claims := ViewerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "tg:" + strconv.FormatInt(telegramID, 10),
			Audience:  jwt.ClaimStrings{viewerAudience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
		},
		TelegramID: telegramID,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(v.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

func (v *ViewerTokens) Parse(tokenString string) (*ViewerClaims, error) {
	claims := &ViewerClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return v.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	audienceOK := false
	for _, a := range claims.Audience {
		if a == viewerAudience {
			audienceOK = true
		}
	}
	if !audienceOK || claims.TelegramID == 0 {
		return nil, errors.New("not a viewer token")
	}
	return claims, nil
}
