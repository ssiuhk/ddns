package frontend

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/pboehm/ddns/admin"
	"github.com/pboehm/ddns/shared"
	"html/template"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type Frontend struct {
	config        *shared.Config
	hosts         shared.HostBackend
	admin         *admin.Admin
	registrations *shared.RateLimiter
}

func NewFrontend(config *shared.Config, hosts shared.HostBackend, adminPanel *admin.Admin) *Frontend {
	return &Frontend{
		config:        config,
		hosts:         hosts,
		admin:         adminPanel,
		registrations: shared.NewRateLimiter(config.RegistrationsPerHour, time.Hour),
	}
}

func (f *Frontend) Run() error {
	return f.Router().Run(f.config.ListenFrontend)
}

// Router builds the HTTP handler of the frontend.
func (f *Frontend) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	if f.config.Verbose {
		r.Use(gin.Logger())
	}

	r.SetHTMLTemplate(buildTemplate())

	if f.admin != nil {
		f.admin.Register(r)
	}

	r.GET("/", func(g *gin.Context) {
		g.HTML(200, "index.html", gin.H{
			"domain":           f.config.Domain,
			"registrationOpen": f.config.RegistrationOpen(),
		})
	})

	r.GET("/available/:hostname", func(c *gin.Context) {
		hostname, valid := isValidHostname(c.Params.ByName("hostname"))

		if valid {
			_, err := f.hosts.GetHost(hostname)
			valid = err != nil
		}

		c.JSON(200, gin.H{
			"available": valid && f.config.RegistrationOpen(),
		})
	})

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(shared.Health(f.hosts, 0))
	})

	r.GET("/new/:hostname", f.register)
	r.GET("/nic/update", f.dynDNS2Update)

	// the token in the path is kept for existing clients; the header keeps it
	// out of access logs
	r.GET("/update/:hostname/:token", func(c *gin.Context) {
		f.update(c, c.Params.ByName("token"))
	})
	r.GET("/update/:hostname", func(c *gin.Context) {
		token, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !found {
			c.JSON(401, gin.H{"error": "Send the token as an Authorization: Bearer header"})
			return
		}
		f.update(c, strings.TrimSpace(token))
	})

	return r
}

func (f *Frontend) register(c *gin.Context) {
	if !f.config.RegistrationOpen() {
		c.JSON(403, gin.H{"error": "Registration is closed, ask the administrator to create the host."})
		return
	}

	hostname, valid := isValidHostname(c.Params.ByName("hostname"))
	if !valid {
		c.JSON(404, gin.H{"error": "This hostname is not valid"})
		return
	}

	address, err := ExtractRemoteAddr(c.Request)
	if err != nil {
		c.JSON(400, gin.H{"error": "Your sender IP address is not in the right format"})
		return
	}
	if !f.registrations.Reserve(address) {
		c.JSON(429, gin.H{"error": "Too many hosts registered from your address, please try again later."})
		return
	}

	host := &shared.Host{Hostname: hostname, IPv4: "127.0.0.1"}
	host.GenerateAndSetToken()
	host.Touch()

	if err = f.hosts.CreateHost(host); err != nil {
		f.registrations.Release(address)
		if errors.Is(err, shared.ErrHostExists) {
			c.JSON(403, gin.H{"error": "This hostname has already been registered."})
		} else {
			c.JSON(400, gin.H{"error": "Could not register host."})
		}
		return
	}

	c.JSON(200, gin.H{
		"hostname":    host.Hostname,
		"token":       host.Token,
		"update_link": fmt.Sprintf("/update/%s/%s", host.Hostname, host.Token),
	})
}

var errWrongToken = errors.New("wrong token")

func (f *Frontend) update(c *gin.Context, token string) {
	hostname, valid := isValidHostname(c.Params.ByName("hostname"))
	if !valid {
		c.JSON(404, gin.H{"error": "This hostname is not valid"})
		return
	}

	addresses, err := requestedAddresses(c)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	host, err := f.hosts.UpdateHost(hostname, func(host *shared.Host) error {
		if !host.HasToken(token) {
			return errWrongToken
		}
		for _, ip := range addresses {
			host.SetIP(ip)
		}
		host.Touch()
		return nil
	})
	switch {
	case errors.Is(err, shared.ErrHostNotFound):
		c.JSON(404, gin.H{
			"error": "This hostname has not been registered or is expired.",
		})
		return
	case errors.Is(err, errWrongToken):
		c.JSON(403, gin.H{
			"error": "You have supplied the wrong token to manipulate this host",
		})
		return
	case err != nil:
		c.JSON(400, gin.H{
			"error": "Could not update registered IP address",
		})
		return
	}

	c.JSON(200, gin.H{
		"current_ip": addresses[0],
		"ipv4":       host.IPv4,
		"ipv6":       host.IPv6,
		"status":     "Successfuly updated",
	})
}

// requestedAddresses returns the addresses a client wants to set: those in
// the myip parameter (comma separated) and myipv6, or else the address the
// request comes from. One IPv4 and one IPv6 address can be set at once.
func requestedAddresses(c *gin.Context) ([]string, error) {
	var values []string
	for _, param := range []string{"myip", "myipv6"} {
		for _, value := range strings.Split(c.Query(param), ",") {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
	}

	if len(values) == 0 {
		ip, err := ExtractRemoteAddr(c.Request)
		if err != nil {
			return nil, errors.New("Your sender IP address is not in the right format")
		}
		values = []string{ip}
	}

	var addresses []string
	var seenIPv4, seenIPv6 bool
	for _, value := range values {
		ip, isIPv4, ok := shared.ParseIP(value)
		if !ok {
			return nil, fmt.Errorf("%q is not an IP address", value)
		}
		if (isIPv4 && seenIPv4) || (!isIPv4 && seenIPv6) {
			return nil, errors.New("Only one IPv4 and one IPv6 address can be set")
		}
		seenIPv4, seenIPv6 = seenIPv4 || isIPv4, seenIPv6 || !isIPv4
		addresses = append(addresses, ip)
	}
	return addresses, nil
}

// ExtractRemoteAddr gets the Remote Address of the client. At First we try to get the
// X-Forwarded-For Header which holds the IP if we are behind a proxy,
// otherwise the RemoteAddr is used
func ExtractRemoteAddr(req *http.Request) (string, error) {
	header_data, ok := req.Header["X-Forwarded-For"]

	if ok {
		// the first entry is the client, any further ones are proxies
		client, _, _ := strings.Cut(header_data[0], ",")
		return strings.TrimSpace(client), nil
	} else {
		ip, _, err := net.SplitHostPort(req.RemoteAddr)
		return ip, err
	}
}

// Get index template from bindata
func buildTemplate() *template.Template {
	html, err := template.New("index.html").Parse(indexTemplate)
	if err != nil {
		log.Fatal(err)
	}

	return html
}

func isValidHostname(host string) (string, bool) {
	return host, shared.IsValidHostname(host)
}
