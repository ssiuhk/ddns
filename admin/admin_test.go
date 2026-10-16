package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ssiuhk/ddns/shared"
	"github.com/stretchr/testify/assert"
)

type memoryStore struct {
	hosts  map[string]*shared.Host
	hash   string
	secret []byte
}

func (m *memoryStore) GetHost(name string) (*shared.Host, error) {
	if host, ok := m.hosts[name]; ok {
		copied := *host
		return &copied, nil
	}
	return nil, errors.New("Host does not exist")
}
func (m *memoryStore) SetHost(host *shared.Host) error {
	copied := *host
	m.hosts[host.Hostname] = &copied
	return nil
}
func (m *memoryStore) ListHosts() ([]*shared.Host, error) {
	var hosts []*shared.Host
	for _, host := range m.hosts {
		hosts = append(hosts, host)
	}
	return hosts, nil
}
func (m *memoryStore) DeleteHost(name string) error { delete(m.hosts, name); return nil }

func (m *memoryStore) GetDomainKeys(string) ([]*shared.DomainKey, error)          { return nil, nil }
func (m *memoryStore) AddDomainKey(string, *shared.DomainKey) (int, error)        { return 0, nil }
func (m *memoryStore) RemoveDomainKey(string, int) error                          { return nil }
func (m *memoryStore) UpdateDomainKey(string, int, func(*shared.DomainKey)) error { return nil }
func (m *memoryStore) GetAllDomainMetadata(string) (map[string][]string, error)   { return nil, nil }
func (m *memoryStore) SetDomainMetadata(string, string, []string) error           { return nil }

func (m *memoryStore) AdminPasswordHash() (string, error) { return m.hash, nil }
func (m *memoryStore) SetAdminPasswordHash(hash string) error {
	m.hash = hash
	m.secret = nil
	return nil
}
func (m *memoryStore) AdminSessionSecret() ([]byte, error) {
	if m.secret == nil {
		m.secret = []byte(randomString())
	}
	return m.secret, nil
}

const testPassword = "correct horse battery"

func setup(t *testing.T) (*gin.Engine, *memoryStore) {
	gin.SetMode(gin.TestMode)

	store := &memoryStore{hosts: map[string]*shared.Host{
		"www": {Hostname: "www", IPv4: "10.0.0.1", Token: "secret"},
	}}
	hash, err := HashPassword(testPassword)
	assert.Nil(t, err)
	store.SetAdminPasswordHash(hash)

	config := &shared.Config{Domain: ".d.example.org", PublicURL: "https://ddns.example.org"}
	remoteAddr := func(r *http.Request) (string, error) { return "192.0.2.1", nil }

	r := gin.New()
	New(config, store, remoteAddr).Register(r)
	return r, store
}

type client struct {
	t       *testing.T
	r       *gin.Engine
	cookies map[string]*http.Cookie
}

func (c *client) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}

	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}

	w := httptest.NewRecorder()
	c.r.ServeHTTP(w, req)

	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(c.cookies, cookie.Name)
		} else {
			c.cookies[cookie.Name] = cookie
		}
	}
	return w
}

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (c *client) csrf() string {
	w := c.do("GET", "/admin", nil)
	match := csrfPattern.FindStringSubmatch(w.Body.String())
	if assert.NotNil(c.t, match, "no csrf token on the page") {
		return match[1]
	}
	return ""
}

func loggedIn(t *testing.T) (*client, *memoryStore) {
	r, store := setup(t)
	c := &client{t: t, r: r, cookies: map[string]*http.Cookie{}}

	w := c.do("POST", "/admin/login", url.Values{"password": {testPassword}})
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin", w.Header().Get("Location"))
	assert.True(t, c.cookies[sessionCookie].HttpOnly)
	assert.True(t, c.cookies[sessionCookie].Secure)

	return c, store
}

func TestRequiresLogin(t *testing.T) {
	r, _ := setup(t)
	c := &client{t: t, r: r, cookies: map[string]*http.Cookie{}}

	w := c.do("GET", "/admin", nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/login", w.Header().Get("Location"))

	w = c.do("POST", "/admin/hosts/www/delete", url.Values{})
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// a tampered session is not accepted
	c.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: "eyJleHAiOjk5OTk5OTk5OTl9.AAAA"}
	w = c.do("GET", "/admin", nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)
}

func TestWrongPasswordAndRateLimit(t *testing.T) {
	r, _ := setup(t)
	c := &client{t: t, r: r, cookies: map[string]*http.Cookie{}}

	for i := 0; i < maxFailedLogins; i++ {
		w := c.do("POST", "/admin/login", url.Values{"password": {"wrong"}})
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	}

	// even the right password is refused once the limit is reached
	w := c.do("POST", "/admin/login", url.Values{"password": {testPassword}})
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Nil(t, c.cookies[sessionCookie])
}

func TestListAndShowHosts(t *testing.T) {
	c, _ := loggedIn(t)

	w := c.do("GET", "/admin", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "www.d.example.org")
	assert.Contains(t, w.Body.String(), "Last updated")
	// hosts saved before the timestamp was tracked
	assert.Contains(t, w.Body.String(), "<td>unknown</td>")
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))

	w = c.do("GET", "/admin/hosts/www", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "https://ddns.example.org/update/www/secret")

	w = c.do("GET", "/admin/hosts/missing", nil)
	assert.Equal(t, "/admin?msg=not-found", w.Header().Get("Location"))
}

func TestCreateUpdateDeleteHost(t *testing.T) {
	c, store := loggedIn(t)
	csrf := c.csrf()

	// forms without the CSRF token are rejected
	w := c.do("POST", "/admin/hosts", url.Values{"hostname": {"pi"}, "ipv4": {"192.0.2.7"}})
	assert.Equal(t, "/admin?msg=bad-request", w.Header().Get("Location"))
	assert.NotContains(t, store.hosts, "pi")

	w = c.do("POST", "/admin/hosts", url.Values{"csrf": {csrf}, "hostname": {"pi"}, "ipv4": {"192.0.2.7"}, "persistent": {"on"}})
	assert.Equal(t, "/admin/hosts/pi?msg=created", w.Header().Get("Location"))
	if assert.Contains(t, store.hosts, "pi") {
		assert.Equal(t, "192.0.2.7", store.hosts["pi"].IPv4)
		assert.True(t, store.hosts["pi"].Persistent)
		assert.NotEmpty(t, store.hosts["pi"].Token)
		assert.InDelta(t, time.Now().Unix(), store.hosts["pi"].UpdatedAt, 5)
	}

	w = c.do("GET", "/admin", nil)
	assert.Regexp(t, `title="\d{4}-\d\d-\d\d \d\d:\d\d:\d\d UTC">a moment ago</td>`, w.Body.String())

	w = c.do("POST", "/admin/hosts", url.Values{"csrf": {csrf}, "hostname": {"pi"}, "ipv4": {"192.0.2.8"}})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "already exists")

	w = c.do("POST", "/admin/hosts", url.Values{"csrf": {csrf}, "hostname": {"bad.name"}, "ipv4": {"192.0.2.8"}})
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = c.do("POST", "/admin/hosts", url.Values{"csrf": {csrf}, "hostname": {"my-nas"}, "ipv4": {"192.0.2.8"}})
	assert.Equal(t, "/admin/hosts/my-nas?msg=created", w.Header().Get("Location"))

	w = c.do("POST", "/admin/hosts/pi", url.Values{"csrf": {csrf}, "ipv4": {"192.0.2.9"}, "ipv6": {"2001:DB8::0001"}})
	assert.Equal(t, "/admin/hosts/pi?msg=updated", w.Header().Get("Location"))
	assert.Equal(t, "192.0.2.9", store.hosts["pi"].IPv4)
	assert.Equal(t, "2001:db8::1", store.hosts["pi"].IPv6)

	// an empty field removes that record, but one address is required
	w = c.do("POST", "/admin/hosts/pi", url.Values{"csrf": {csrf}, "ipv6": {"2001:db8::1"}})
	assert.Equal(t, "/admin/hosts/pi?msg=updated", w.Header().Get("Location"))
	assert.Equal(t, "", store.hosts["pi"].IPv4)
	w = c.do("POST", "/admin/hosts/pi", url.Values{"csrf": {csrf}})
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// each field only takes its own family
	w = c.do("POST", "/admin/hosts/pi", url.Values{"csrf": {csrf}, "ipv4": {"2001:db8::2"}})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, store.hosts["pi"].Persistent)

	w = c.do("POST", "/admin/hosts/pi", url.Values{"csrf": {csrf}, "ipv4": {"not an ip"}})
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// a new token does not change the record, so it keeps its timestamp
	oldToken := store.hosts["pi"].Token
	store.hosts["pi"].UpdatedAt = 1700000000
	c.do("POST", "/admin/hosts/pi/token", url.Values{"csrf": {csrf}})
	assert.NotEqual(t, oldToken, store.hosts["pi"].Token)
	assert.Equal(t, int64(1700000000), store.hosts["pi"].UpdatedAt)

	w = c.do("GET", "/admin/hosts/pi", nil)
	assert.Contains(t, w.Body.String(), "(2023-11-14 22:13:20 UTC)")

	w = c.do("POST", "/admin/hosts/pi/delete", url.Values{"csrf": {csrf}})
	assert.Equal(t, "/admin?msg=deleted", w.Header().Get("Location"))
	assert.NotContains(t, store.hosts, "pi")
}

func TestPasswordResetSignsOut(t *testing.T) {
	c, store := loggedIn(t)

	hash, _ := HashPassword("a brand new password")
	store.SetAdminPasswordHash(hash)

	w := c.do("GET", "/admin", nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/login", w.Header().Get("Location"))
}

func TestLogout(t *testing.T) {
	c, _ := loggedIn(t)

	w := c.do("POST", "/admin/logout", url.Values{"csrf": {c.csrf()}})
	assert.Equal(t, "/admin/login?msg=logged-out", w.Header().Get("Location"))

	w = c.do("GET", "/admin", nil)
	assert.Equal(t, http.StatusSeeOther, w.Code)
}

func TestGoogleLoginRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &memoryStore{hosts: map[string]*shared.Host{}}
	config := &shared.Config{
		Domain: ".d.example.org", PublicURL: "https://ddns.example.org",
		GoogleClientID: "id", GoogleClientSecret: "secret", AdminEmail: "admin@example.org",
	}
	r := gin.New()
	New(config, store, func(*http.Request) (string, error) { return "", nil }).Register(r)
	c := &client{t: t, r: r, cookies: map[string]*http.Cookie{}}

	w := c.do("GET", "/admin/login", nil)
	assert.Contains(t, w.Body.String(), "Sign in with Google")
	assert.NotContains(t, w.Body.String(), `type="password"`)

	w = c.do("GET", "/admin/login/google", nil)
	location, _ := url.Parse(w.Header().Get("Location"))
	assert.Equal(t, "accounts.google.com", location.Host)
	assert.Equal(t, "https://ddns.example.org/admin/login/google/callback", location.Query().Get("redirect_uri"))
	assert.Equal(t, "S256", location.Query().Get("code_challenge_method"))
	assert.NotNil(t, c.cookies[oauthCookie])

	// a callback with a state that does not match is refused
	w = c.do("GET", "/admin/login/google/callback?state=forged&code=x", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Nil(t, c.cookies[sessionCookie])
}
