package frontend

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// dynDNS2Update implements the update protocol of DynDNS, which routers and
// clients like ddclient speak:
//
//	GET /nic/update?hostname=<host>[,<host>...]&myip=<ip>[,<ip>]
//
// with basic authentication, where the password is the token of the host.
// Hostnames can be given with or without the domain; without a hostname
// parameter the user name is the host. Each host gets one line in the plain
// text answer: "good <ip>", "nochg <ip>", "nohost", "badauth" or "notfqdn".
func (f *Frontend) dynDNS2Update(c *gin.Context) {
	user, token, ok := c.Request.BasicAuth()
	if !ok {
		c.Header("WWW-Authenticate", `Basic realm="ddns"`)
		c.String(http.StatusUnauthorized, "badauth\n")
		return
	}

	names := c.Query("hostname")
	if names == "" {
		names = user
	}

	addresses, err := requestedAddresses(c)
	if err != nil {
		// the protocol has no answer for a bad address, this is the closest
		c.String(http.StatusOK, "dnserr\n")
		return
	}

	var answers []string
	authFailed := false
	for _, name := range strings.Split(names, ",") {
		hostname, valid := f.hostnameOf(strings.TrimSpace(name))
		if !valid {
			answers = append(answers, "notfqdn")
			continue
		}

		host, err := f.hosts.GetHost(hostname)
		if err != nil {
			answers = append(answers, "nohost")
			continue
		}
		if !host.HasToken(token) {
			authFailed = true
			answers = append(answers, "badauth")
			continue
		}

		before := strings.Join(host.Addresses(), ",")
		for _, ip := range addresses {
			host.SetIP(ip)
		}
		host.Touch()
		if err := f.hosts.SetHost(host); err != nil {
			answers = append(answers, "911")
			continue
		}

		after := strings.Join(host.Addresses(), ",")
		if before == after {
			answers = append(answers, "nochg "+after)
		} else {
			answers = append(answers, "good "+after)
		}
	}

	status := http.StatusOK
	if authFailed && len(answers) == 1 {
		status = http.StatusUnauthorized
	}
	c.String(status, fmt.Sprintln(strings.Join(answers, "\n")))
}

// hostnameOf returns the host part of name, which may be a full name in our
// domain (pi.d.example.org) or just the host (pi).
func (f *Frontend) hostnameOf(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	name = strings.TrimSuffix(name, f.config.Domain)
	return isValidHostname(name)
}
