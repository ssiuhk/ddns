package shared

import (
	"crypto/sha1"
	"fmt"
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

func (h *Host) GenerateAndSetToken() {
	hash := sha1.New()
	hash.Write([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	hash.Write([]byte(h.Hostname))

	h.Token = fmt.Sprintf("%x", hash.Sum(nil))
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
