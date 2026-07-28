package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const cookieName = "__Host-webconsole_csrf"

type Protector struct {
	secret       []byte
	publicOrigin string
	now          func() time.Time
}

func NewProtector(secret []byte, publicOrigin string) *Protector {
	return &Protector{
		secret:       append([]byte(nil), secret...),
		publicOrigin: strings.TrimRight(publicOrigin, "/"),
		now:          time.Now,
	}
}

func (p *Protector) Issue(writer http.ResponseWriter) (string, error) {
	session := make([]byte, 32)
	if _, err := rand.Read(session); err != nil {
		return "", fmt.Errorf("generate CSRF session: %w", err)
	}
	sessionValue := base64.RawURLEncoding.EncodeToString(session)
	http.SetCookie(writer, &http.Cookie{
		Name:     cookieName,
		Value:    sessionValue,
		Path:     "/",
		MaxAge:   int((8 * time.Hour).Seconds()),
		Expires:  p.now().Add(8 * time.Hour),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	return p.token(sessionValue), nil
}

func (p *Protector) Validate(request *http.Request) error {
	if err := p.validateOrigin(request); err != nil {
		return err
	}
	cookie, err := request.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return errors.New("missing CSRF cookie")
	}
	provided := request.Header.Get("X-CSRF-Token")
	expected := p.token(cookie.Value)
	if subtle.ConstantTimeCompare(
		[]byte(provided),
		[]byte(expected),
	) != 1 {
		return errors.New("invalid CSRF token")
	}
	return nil
}

func (p *Protector) token(session string) string {
	mac := hmac.New(sha256.New, p.secret)
	_, _ = mac.Write([]byte("csrf\x00" + session))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *Protector) validateOrigin(request *http.Request) error {
	rawOrigin := request.Header.Get("Origin")
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return errors.New("missing or invalid Origin")
	}
	actual := origin.Scheme + "://" + origin.Host
	if p.publicOrigin != "" {
		if actual != p.publicOrigin {
			return errors.New("Origin is not allowed")
		}
		return nil
	}
	proto := request.Header.Get("X-Forwarded-Proto")
	if proto == "" && request.TLS != nil {
		proto = "https"
	}
	if proto != "https" || origin.Host != request.Host {
		return errors.New("Origin does not match HTTPS request host")
	}
	return nil
}

type ConfirmationClaims struct {
	User            string
	CutoffMS        int64
	ResponseCount   int64
	DiagnosticCount int64
	ExpiresAt       time.Time
}

type ConfirmationStore struct {
	mu     sync.Mutex
	values map[string]ConfirmationClaims
	now    func() time.Time
}

func NewConfirmationStore() *ConfirmationStore {
	return &ConfirmationStore{
		values: make(map[string]ConfirmationClaims),
		now:    time.Now,
	}
}

func (s *ConfirmationStore) Issue(
	claims ConfirmationClaims,
) (string, error) {
	randomValue := make([]byte, 32)
	if _, err := rand.Read(randomValue); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(randomValue)
	claims.ExpiresAt = s.now().Add(5 * time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked()
	s.values[token] = claims
	return token, nil
}

func (s *ConfirmationStore) Consume(
	token, user string,
	cutoffMS int64,
) (ConfirmationClaims, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked()
	claims, ok := s.values[token]
	if !ok {
		return ConfirmationClaims{}, errors.New(
			"invalid or expired confirmation token",
		)
	}
	delete(s.values, token)
	if claims.User != user || claims.CutoffMS != cutoffMS {
		return ConfirmationClaims{}, errors.New(
			"confirmation token does not match this cleanup",
		)
	}
	return claims, nil
}

func (s *ConfirmationStore) removeExpiredLocked() {
	now := s.now()
	for token, claims := range s.values {
		if !claims.ExpiresAt.After(now) {
			delete(s.values, token)
		}
	}
}

func AuthenticatedUser(request *http.Request) string {
	if username := request.Header.Get("X-WebConsole-User"); username != "" {
		return username
	}
	if username, _, ok := request.BasicAuth(); ok {
		return username
	}
	return ""
}
