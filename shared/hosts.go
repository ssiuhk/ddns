package shared

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

type Host struct {
	Hostname   string `redis:"-"`
	Ip         string `redis:"ip"`
	Token      string `redis:"token"`
	Persistent bool   `redis:"persistent"`

	// UpdatedAt is when the record was last set, as a Unix time; zero for
	// hosts saved before it was tracked.
	UpdatedAt int64 `redis:"updated"`

	// ExpiresIn is how long the host is kept without an update; zero for
	// persistent hosts. It is only filled in when reading hosts.
	ExpiresIn time.Duration `redis:"-"`
}

// GenerateAndSetToken gives the host a new random token of 256 bits.
func (h *Host) GenerateAndSetToken() {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		panic(err)
	}
	h.Token = hex.EncodeToString(token)
}

// HasToken compares token with the host's token in constant time.
func (h *Host) HasToken(token string) bool {
	return h.Token != "" && subtle.ConstantTimeCompare([]byte(h.Token), []byte(token)) == 1
}

// Touch records that the record has just been set.
func (h *Host) Touch() {
	h.UpdatedAt = time.Now().Unix()
}

func (h *Host) IsIPv4() bool {
	if strings.Contains(h.Ip, ".") {
		return true
	}

	return false
}

var hostnamePattern = regexp.MustCompile("^([a-zA-Z0-9]([a-zA-Z0-9\\-]{0,61}[a-zA-Z0-9])?)$")

// IsValidHostname reports whether name can be registered as a host.
func IsValidHostname(name string) bool {
	return hostnamePattern.MatchString(name)
}

type HostBackend interface {
	GetHost(string) (*Host, error)

	SetHost(*Host) error
}

// HostManager is a HostBackend that can also enumerate and remove hosts.
type HostManager interface {
	HostBackend

	ListHosts() ([]*Host, error)

	DeleteHost(string) error
}
