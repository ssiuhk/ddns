package shared

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"regexp"
	"strings"
	"time"
)

type Host struct {
	Hostname string `redis:"-"`
	// IPv4 and IPv6 are served as the A and AAAA record of the host; either
	// may be empty.
	IPv4       string `redis:"ip4"`
	IPv6       string `redis:"ip6"`
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

// ParseIP returns the canonical form of an IPv4 or IPv6 address and
// whether it is an IPv4 one.
func ParseIP(value string) (ip string, isIPv4 bool, ok bool) {
	parsed := net.ParseIP(strings.TrimSpace(value))
	if parsed == nil {
		return "", false, false
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String(), true, true
	}
	return parsed.String(), false, true
}

// SetIP sets the address of the family of ip, keeping the other one.
func (h *Host) SetIP(ip string) bool {
	canonical, isIPv4, ok := ParseIP(ip)
	if !ok {
		return false
	}
	if isIPv4 {
		h.IPv4 = canonical
	} else {
		h.IPv6 = canonical
	}
	return true
}

// Addresses lists the addresses of the host, IPv4 first.
func (h *Host) Addresses() []string {
	var addresses []string
	for _, ip := range []string{h.IPv4, h.IPv6} {
		if ip != "" {
			addresses = append(addresses, ip)
		}
	}
	return addresses
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
