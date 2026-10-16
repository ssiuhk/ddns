package frontend

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ssiuhk/ddns/shared"
	"github.com/stretchr/testify/assert"
)

type memoryHosts map[string]*shared.Host

func (m memoryHosts) GetHost(name string) (*shared.Host, error) {
	if host, ok := m[name]; ok {
		copied := *host
		return &copied, nil
	}
	return nil, errors.New("Host does not exist")
}

func (m memoryHosts) SetHost(host *shared.Host) error {
	copied := *host
	m[host.Hostname] = &copied
	return nil
}

func newFrontend(registration string, perHour int) (*gin.Engine, memoryHosts) {
	gin.SetMode(gin.TestMode)
	hosts := memoryHosts{}
	config := &shared.Config{Domain: ".d.example.org", Registration: registration, RegistrationsPerHour: perHour}
	return NewFrontend(config, hosts, nil).Router(), hosts
}

func get(r *gin.Engine, path string, header ...string) (*httptest.ResponseRecorder, map[string]interface{}) {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "192.0.2.10:4711"
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := map[string]interface{}{}
	json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestRegisterAndUpdate(t *testing.T) {
	r, hosts := newFrontend("open", 5)

	w, body := get(r, "/new/pi")
	assert.Equal(t, http.StatusOK, w.Code)
	token := body["token"].(string)
	assert.Regexp(t, "^[0-9a-f]{64}$", token)
	assert.NotZero(t, hosts["pi"].UpdatedAt)

	w, _ = get(r, "/update/pi/wrong")
	assert.Equal(t, http.StatusForbidden, w.Code)

	w, body = get(r, "/update/pi/"+token)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "192.0.2.10", hosts["pi"].IPv4)

	w, _ = get(r, "/update/pi")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w, _ = get(r, "/update/pi", "Authorization", "Bearer wrong")
	assert.Equal(t, http.StatusForbidden, w.Code)

	w, _ = get(r, "/update/pi", "Authorization", "Bearer "+token, "X-Forwarded-For", "198.51.100.7")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "198.51.100.7", hosts["pi"].IPv4)
}

func TestRegistrationRateLimit(t *testing.T) {
	r, hosts := newFrontend("open", 2)

	for _, name := range []string{"a", "b"} {
		w, _ := get(r, "/new/"+name)
		assert.Equal(t, http.StatusOK, w.Code)
	}
	w, _ := get(r, "/new/c")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.NotContains(t, hosts, "c")

	// failed registrations do not count
	r, _ = newFrontend("open", 1)
	get(r, "/new/bad.name")
	w, _ = get(r, "/new/ok")
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestClosedRegistration(t *testing.T) {
	r, hosts := newFrontend("closed", 5)

	w, _ := get(r, "/new/pi")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, hosts)

	_, body := get(r, "/available/pi")
	assert.Equal(t, false, body["available"])

	w, _ = get(r, "/")
	assert.Contains(t, w.Body.String(), "Registration is closed")
	assert.NotContains(t, w.Body.String(), `id="register"`)
}

func TestUpdateKeepsBothFamilies(t *testing.T) {
	r, hosts := newFrontend("open", 5)
	_, body := get(r, "/new/pi")
	token := body["token"].(string)

	get(r, "/update/pi/"+token)
	w, _ := get(r, "/update/pi/"+token, "X-Forwarded-For", "2001:db8::7, 10.0.0.1")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "192.0.2.10", hosts["pi"].IPv4)
	assert.Equal(t, "2001:db8::7", hosts["pi"].IPv6)

	w, _ = get(r, "/update/pi?myip=198.51.100.1,2001:db8::8", "Authorization", "Bearer "+token)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "198.51.100.1", hosts["pi"].IPv4)
	assert.Equal(t, "2001:db8::8", hosts["pi"].IPv6)

	w, _ = get(r, "/update/pi/"+token+"?myip=10.0.0.1,10.0.0.2")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w, _ = get(r, "/update/pi/"+token+"?myip=nonsense")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func dyndns(r *gin.Engine, path, user, password string) (int, string) {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "192.0.2.10:4711"
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestDynDNS2(t *testing.T) {
	r, hosts := newFrontend("open", 5)
	_, body := get(r, "/new/pi")
	token := body["token"].(string)
	_, body = get(r, "/new/nas")
	nasToken := body["token"].(string)

	code, answer := dyndns(r, "/nic/update?hostname=pi.d.example.org", "", "")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "badauth\n", answer)

	code, answer = dyndns(r, "/nic/update?hostname=pi.d.example.org", "pi", "wrong")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "badauth\n", answer)

	code, answer = dyndns(r, "/nic/update?hostname=pi.d.example.org&myip=203.0.113.5", "pi", token)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "good 203.0.113.5\n", answer)
	assert.Equal(t, "203.0.113.5", hosts["pi"].IPv4)

	_, answer = dyndns(r, "/nic/update?hostname=pi&myip=203.0.113.5", "pi", token)
	assert.Equal(t, "nochg 203.0.113.5\n", answer)

	// a router sending both addresses, the user name as the host
	_, answer = dyndns(r, "/nic/update?myip=203.0.113.6&myipv6=2001:db8::6", "pi.d.example.org", token)
	assert.Equal(t, "good 203.0.113.6,2001:db8::6\n", answer)

	// several hosts in one request; only those with the token change
	code, answer = dyndns(r, "/nic/update?hostname=pi,nas,missing,bad.name.example.com", "x", token)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "good 192.0.2.10,2001:db8::6\nbadauth\nnohost\nnotfqdn\n", answer)
	assert.Equal(t, "127.0.0.1", hosts["nas"].IPv4)

	_, answer = dyndns(r, "/nic/update?hostname=nas.d.example.org.", "nas", nasToken)
	assert.Equal(t, "good 192.0.2.10\n", answer)

	_, answer = dyndns(r, "/nic/update?hostname=pi&myip=junk", "pi", token)
	assert.Equal(t, "dnserr\n", answer)
}
