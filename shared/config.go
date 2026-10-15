package shared

import (
	"flag"
	"log"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Verbose            bool
	DNSSEC             bool
	Domain             string
	SOAFqdn            string
	HostExpirationDays int
	ListenFrontend     string
	ListenBackend      string
	RedisHost          string

	// Admin panel
	PublicURL          string
	AdminEmail         string
	GoogleClientID     string
	GoogleClientSecret string
}

func (c *Config) Initialize() {
	flag.StringVar(&c.Domain, "domain", "",
		"The subdomain which should be handled by DDNS")

	flag.StringVar(&c.SOAFqdn, "soa_fqdn", "",
		"The FQDN of the DNS server which is returned as a SOA record")

	flag.StringVar(&c.ListenBackend, "listen-backend", ":8053",
		"Which socket should the backend web service use to bind itself")

	flag.StringVar(&c.ListenFrontend, "listen-frontend", ":8080",
		"Which socket should the frontend web service use to bind itself")

	flag.StringVar(&c.RedisHost, "redis", RedisHostDefault(),
		"The Redis socket that should be used (env DDNS_REDIS_HOST)")

	flag.IntVar(&c.HostExpirationDays, "expiration-days", 10,
		"The number of days after a host is released when it is not updated")

	flag.BoolVar(&c.DNSSEC, "dnssec", true,
		"Sign the zone with DNSSEC, creating a key on startup if there is none")

	flag.StringVar(&c.PublicURL, "public-url", os.Getenv("DDNS_PUBLIC_URL"),
		"The URL the frontend is reachable at, e.g. https://ddns.example.org (env DDNS_PUBLIC_URL)")

	flag.StringVar(&c.AdminEmail, "admin-email", os.Getenv("DDNS_ADMIN_EMAIL"),
		"The Google account allowed to sign in to the admin panel (env DDNS_ADMIN_EMAIL)")

	flag.StringVar(&c.GoogleClientID, "google-client-id", os.Getenv("DDNS_GOOGLE_CLIENT_ID"),
		"OAuth client ID for signing in to the admin panel with Google (env DDNS_GOOGLE_CLIENT_ID)")

	flag.StringVar(&c.GoogleClientSecret, "google-client-secret", os.Getenv("DDNS_GOOGLE_CLIENT_SECRET"),
		"OAuth client secret for signing in to the admin panel with Google (env DDNS_GOOGLE_CLIENT_SECRET)")

	flag.BoolVar(&c.Verbose, "verbose", false,
		"Be more verbose")
}

func (c *Config) Validate() {
	flag.Parse()

	c.Domain = strings.ToLower(strings.TrimSuffix(c.Domain, "."))
	c.SOAFqdn = strings.ToLower(strings.TrimSuffix(c.SOAFqdn, "."))

	if c.Domain == "" {
		log.Fatal("You have to supply the domain via --domain=DOMAIN")
	} else if !strings.HasPrefix(c.Domain, ".") {
		// get the domain in the right format
		c.Domain = "." + c.Domain
	}

	if c.SOAFqdn == "" {
		log.Fatal("You have to supply the server FQDN via --soa_fqdn=FQDN")
	}

	c.PublicURL = strings.TrimSuffix(c.PublicURL, "/")
	if c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || u.Host == "" {
			log.Fatal("--public-url has to be an absolute URL like https://ddns.example.org")
		}
	}

	if c.GoogleLoginEnabled() && (c.AdminEmail == "" || c.PublicURL == "") {
		log.Fatal("Google sign in needs --admin-email and --public-url as well")
	}
}

// GoogleLoginEnabled reports whether signing in with Google is configured.
func (c *Config) GoogleLoginEnabled() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

// RedisHostDefault is the Redis address from DDNS_REDIS_HOST, or the local default.
func RedisHostDefault() string {
	if host := os.Getenv("DDNS_REDIS_HOST"); host != "" {
		return host
	}
	return ":6379"
}

// Zone returns the name of the zone we are authoritative for, without the
// leading and trailing dot: d.example.org
func (c *Config) Zone() string {
	return strings.TrimPrefix(c.Domain, ".")
}

// NormalizeName lowercases a DNS name and strips its trailing dot.
func NormalizeName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}
