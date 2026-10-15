package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
)

const (
	sessionCookie  = "ddns_admin"
	sessionMaxAge  = 12 * time.Hour
	oauthCookie    = "ddns_oauth"
	oauthMaxAge    = 10 * time.Minute
	googleUserInfo = "https://openidconnect.googleapis.com/v1/userinfo"

	maxFailedLogins    = 10
	failedLoginsWindow = 15 * time.Minute
)

var googleEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

type session struct {
	Expires int64  `json:"exp"`
	Method  string `json:"via"`
	Nonce   string `json:"n"`
}

// sign returns payload.signature, both base64url encoded, using HMAC-SHA256.
func sign(secret []byte, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verify returns the payload of a value created by sign, if the signature is valid.
func verify(secret []byte, value string) ([]byte, bool) {
	encodedPayload, encodedSignature, found := strings.Cut(value, ".")
	if !found {
		return nil, false
	}

	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return nil, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return nil, false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return payload, hmac.Equal(signature, mac.Sum(nil))
}

func randomString() string {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

// currentSession returns the valid session of the request, if any.
func (a *Admin) currentSession(c *gin.Context) (*session, []byte) {
	value, err := c.Cookie(sessionCookie)
	if err != nil {
		return nil, nil
	}

	secret, err := a.store.AdminSessionSecret()
	if err != nil {
		return nil, nil
	}

	payload, ok := verify(secret, value)
	if !ok {
		return nil, nil
	}

	s := &session{}
	if err := json.Unmarshal(payload, s); err != nil || time.Now().Unix() > s.Expires {
		return nil, nil
	}

	return s, secret
}

func (a *Admin) startSession(c *gin.Context, method string) error {
	secret, err := a.store.AdminSessionSecret()
	if err != nil {
		return err
	}

	payload, _ := json.Marshal(&session{
		Expires: time.Now().Add(sessionMaxAge).Unix(),
		Method:  method,
		Nonce:   randomString(),
	})

	a.setCookie(c, sessionCookie, sign(secret, payload), sessionMaxAge, "/admin")
	return nil
}

func (a *Admin) setCookie(c *gin.Context, name, value string, maxAge time.Duration, path string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   a.secureCookies(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *Admin) clearCookie(c *gin.Context, name, path string) {
	a.setCookie(c, name, "", -time.Second, path)
}

func (a *Admin) secureCookies(c *gin.Context) bool {
	return strings.HasPrefix(a.config.PublicURL, "https://") ||
		c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
}

// csrfToken binds form submissions to the session they were rendered for.
func csrfToken(secret []byte, s *session) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("csrf|" + s.Nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// checkPassword compares a password with the stored bcrypt hash.
func (a *Admin) checkPassword(password string) (bool, error) {
	hash, err := a.store.AdminPasswordHash()
	if err != nil || hash == "" {
		return false, err
	}

	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return false, nil
	}
	return err == nil, err
}

// HashPassword creates the bcrypt hash stored for the admin password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// loginLimiter slows down password guessing by refusing further attempts
// from an address after too many failures.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: map[string][]time.Time{}}
}

func (l *loginLimiter) recent(address string, now time.Time) []time.Time {
	var recent []time.Time
	for _, t := range l.failures[address] {
		if now.Sub(t) < failedLoginsWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.failures, address)
	} else {
		l.failures[address] = recent
	}
	return recent
}

func (l *loginLimiter) Allowed(address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(address, time.Now())) < maxFailedLogins
}

func (l *loginLimiter) Failed(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.failures[address] = append(l.recent(address, now), now)
}

func (l *loginLimiter) Succeeded(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, address)
}

func (a *Admin) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.config.GoogleClientID,
		ClientSecret: a.config.GoogleClientSecret,
		Endpoint:     googleEndpoint,
		RedirectURL:  a.config.PublicURL + "/admin/login/google/callback",
		Scopes:       []string{"openid", "email"},
	}
}

type oauthState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Expires  int64  `json:"exp"`
}

// googleRedirect starts the authorization code flow with PKCE.
func (a *Admin) googleRedirect(c *gin.Context) (string, error) {
	secret, err := a.store.AdminSessionSecret()
	if err != nil {
		return "", err
	}

	state := &oauthState{
		State:    randomString(),
		Verifier: oauth2.GenerateVerifier(),
		Expires:  time.Now().Add(oauthMaxAge).Unix(),
	}
	payload, _ := json.Marshal(state)
	a.setCookie(c, oauthCookie, sign(secret, payload), oauthMaxAge, "/admin/login/google")

	return a.oauthConfig().AuthCodeURL(state.State,
		oauth2.S256ChallengeOption(state.Verifier),
		oauth2.SetAuthURLParam("prompt", "select_account")), nil
}

// googleCallback finishes the flow and returns the verified email address
// of the Google account that signed in.
func (a *Admin) googleCallback(c *gin.Context) (string, error) {
	value, err := c.Cookie(oauthCookie)
	a.clearCookie(c, oauthCookie, "/admin/login/google")
	if err != nil {
		return "", errors.New("the sign in has expired, please try again")
	}

	secret, err := a.store.AdminSessionSecret()
	if err != nil {
		return "", err
	}
	payload, ok := verify(secret, value)
	state := &oauthState{}
	if !ok || json.Unmarshal(payload, state) != nil || time.Now().Unix() > state.Expires {
		return "", errors.New("the sign in has expired, please try again")
	}
	if !hmac.Equal([]byte(state.State), []byte(c.Query("state"))) {
		return "", errors.New("the sign in request did not match, please try again")
	}
	if c.Query("error") != "" || c.Query("code") == "" {
		return "", errors.New("Google did not sign you in")
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	config := a.oauthConfig()
	token, err := config.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(state.Verifier))
	if err != nil {
		return "", errors.New("could not complete the sign in with Google")
	}

	response, err := config.Client(ctx, token).Get(googleUserInfo)
	if err != nil {
		return "", errors.New("could not read your Google account")
	}
	defer response.Body.Close()

	var info struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&info) != nil {
		return "", errors.New("could not read your Google account")
	}
	if !info.EmailVerified {
		return "", errors.New("the email address of your Google account is not verified")
	}

	return info.Email, nil
}
