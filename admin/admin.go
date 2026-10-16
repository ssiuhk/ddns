// Package admin is the control panel for the single administrator of a ddns
// instance, who can list, show, create, update and delete hosts.
package admin

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pboehm/ddns/shared"
)

// Store is everything the admin panel reads and writes.
type Store interface {
	shared.HostManager
	shared.KeyStore

	AdminPasswordHash() (string, error)
	SetAdminPasswordHash(string) error
	AdminSessionSecret() ([]byte, error)
}

type Admin struct {
	config     *shared.Config
	store      Store
	remoteAddr func(*http.Request) (string, error)
	limiter    *shared.RateLimiter
}

func New(config *shared.Config, store Store, remoteAddr func(*http.Request) (string, error)) *Admin {
	return &Admin{config: config, store: store, remoteAddr: remoteAddr, limiter: shared.NewRateLimiter(maxFailedLogins, failedLoginsWindow)}
}

// messages are the notices that can be shown after a redirect, by key, so
// that a link cannot put arbitrary text on the page.
var messages = map[string]string{
	"created":     "The host has been created.",
	"updated":     "The host has been updated.",
	"token":       "A new token has been generated. The old update URL no longer works.",
	"deleted":     "The host has been deleted.",
	"logged-out":  "You have been signed out.",
	"not-found":   "This host does not exist or has expired.",
	"bad-request": "The form has expired, please try again.",
}

// Register adds the admin routes to the frontend router.
func (a *Admin) Register(r *gin.Engine) {
	group := r.Group("/admin", securityHeaders)

	group.GET("/login", a.loginPage)
	group.POST("/login", a.passwordLogin)
	group.GET("/login/google", a.googleLogin)
	group.GET("/login/google/callback", a.googleLoginCallback)

	authed := group.Group("", a.requireSession)
	authed.GET("", a.listHosts)
	authed.POST("/logout", a.logout)
	authed.POST("/hosts", a.createHost)
	authed.GET("/hosts/:hostname", a.showHost)
	authed.POST("/hosts/:hostname", a.updateHost)
	authed.POST("/hosts/:hostname/token", a.regenerateToken)
	authed.POST("/hosts/:hostname/delete", a.deleteHost)
}

func securityHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Frame-Options", "DENY")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "same-origin")
	c.Header("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://accounts.google.com; frame-ancestors 'none'; base-uri 'none'")
}

// requireSession lets only signed in requests through, and checks the CSRF
// token of every form submission.
func (a *Admin) requireSession(c *gin.Context) {
	s, secret := a.currentSession(c)
	if s == nil {
		if c.Request.Method == http.MethodGet {
			c.Redirect(http.StatusSeeOther, "/admin/login")
		} else {
			c.String(http.StatusUnauthorized, "Please sign in again")
		}
		c.Abort()
		return
	}

	token := csrfToken(secret, s)
	if c.Request.Method == http.MethodPost && c.PostForm("csrf") != token {
		c.Redirect(http.StatusSeeOther, "/admin?msg=bad-request")
		c.Abort()
		return
	}

	c.Set("csrf", token)
	c.Next()
}

func (a *Admin) render(c *gin.Context, status int, page string, data gin.H) {
	data["Zone"] = a.config.Zone()
	data["CSRF"] = c.GetString("csrf")
	data["Message"] = messages[c.Query("msg")]

	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(c.Writer, page, data); err != nil {
		log.Printf("Could not render admin page %s: %v", page, err)
	}
}

func (a *Admin) loginPage(c *gin.Context) {
	if s, _ := a.currentSession(c); s != nil {
		c.Redirect(http.StatusSeeOther, "/admin")
		return
	}
	a.renderLogin(c, http.StatusOK, "")
}

func (a *Admin) renderLogin(c *gin.Context, status int, loginError string) {
	hash, err := a.store.AdminPasswordHash()
	if err != nil {
		log.Printf("Could not read the admin password: %v", err)
	}

	a.render(c, status, "login.html", gin.H{
		"Title":           "Sign in",
		"Error":           loginError,
		"PasswordEnabled": hash != "",
		"GoogleEnabled":   a.config.GoogleLoginEnabled(),
	})
}

func (a *Admin) passwordLogin(c *gin.Context) {
	address, _ := a.remoteAddr(c.Request)

	if !a.limiter.Allow(address) {
		a.renderLogin(c, http.StatusTooManyRequests, "Too many failed attempts, please wait a few minutes.")
		return
	}

	ok, err := a.checkPassword(c.PostForm("password"))
	if err != nil {
		log.Printf("Could not check the admin password: %v", err)
	}
	if !ok {
		a.limiter.Add(address)
		log.Printf("Failed admin login from %s", address)
		a.renderLogin(c, http.StatusUnauthorized, "The password is not correct.")
		return
	}

	a.limiter.Reset(address)
	a.signIn(c, "password")
}

func (a *Admin) googleLogin(c *gin.Context) {
	if !a.config.GoogleLoginEnabled() {
		c.Redirect(http.StatusSeeOther, "/admin/login")
		return
	}

	url, err := a.googleRedirect(c)
	if err != nil {
		a.renderLogin(c, http.StatusInternalServerError, "Could not start the sign in with Google.")
		return
	}
	c.Redirect(http.StatusSeeOther, url)
}

func (a *Admin) googleLoginCallback(c *gin.Context) {
	if !a.config.GoogleLoginEnabled() {
		c.Redirect(http.StatusSeeOther, "/admin/login")
		return
	}

	email, err := a.googleCallback(c)
	if err != nil {
		a.renderLogin(c, http.StatusUnauthorized, "Sign in with Google failed: "+err.Error())
		return
	}
	if !strings.EqualFold(email, a.config.AdminEmail) {
		address, _ := a.remoteAddr(c.Request)
		log.Printf("Refused admin login of Google account %s from %s", email, address)
		a.renderLogin(c, http.StatusForbidden, "The Google account "+email+" is not allowed to manage this server.")
		return
	}

	a.signIn(c, "google")
}

func (a *Admin) signIn(c *gin.Context, method string) {
	if err := a.startSession(c, method); err != nil {
		log.Printf("Could not start an admin session: %v", err)
		a.renderLogin(c, http.StatusInternalServerError, "Could not sign you in, please try again.")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin")
}

func (a *Admin) logout(c *gin.Context) {
	a.clearCookie(c, sessionCookie, "/admin")
	c.Redirect(http.StatusSeeOther, "/admin/login?msg=logged-out")
}

type hostView struct {
	*shared.Host
	FQDN    string
	Type    string
	Expires string
	// Updated says how long ago the record was set, UpdatedTime when exactly
	Updated     string
	UpdatedTime string
}

func (a *Admin) view(host *shared.Host) *hostView {
	recordType := "A"
	if !host.IsIPv4() {
		recordType = "AAAA"
	}

	expires := "never"
	if !host.Persistent {
		expires = "in " + humanDuration(host.ExpiresIn)
	}

	updated, updatedTime := "unknown", ""
	if host.UpdatedAt > 0 {
		at := time.Unix(host.UpdatedAt, 0).UTC()
		updated = humanDuration(time.Since(at)) + " ago"
		updatedTime = at.Format("2006-01-02 15:04:05 UTC")
	}

	return &hostView{
		Host: host, FQDN: host.Hostname + a.config.Domain, Type: recordType, Expires: expires,
		Updated: updated, UpdatedTime: updatedTime,
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		return "a moment"
	}
}

func (a *Admin) listHosts(c *gin.Context) {
	hosts, err := a.store.ListHosts()
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not list hosts: %v", err)
		return
	}

	filter := strings.ToLower(strings.TrimSpace(c.Query("q")))
	var views []*hostView
	for _, host := range hosts {
		if filter == "" || strings.Contains(host.Hostname, filter) || strings.Contains(host.Ip, filter) {
			views = append(views, a.view(host))
		}
	}

	var dsRecords []string
	if a.config.DNSSEC {
		records, err := shared.DSRecords(a.store, a.config.Zone())
		if err != nil {
			log.Printf("Could not read the DNSSEC keys: %v", err)
		}
		for _, ds := range records {
			dsRecords = append(dsRecords, strings.ReplaceAll(ds.String(), "\t", " "))
		}
	}

	a.render(c, http.StatusOK, "hosts.html", gin.H{
		"Title":     "Hosts",
		"Hosts":     views,
		"Total":     len(hosts),
		"Filter":    filter,
		"DNSSEC":    a.config.DNSSEC,
		"DSRecords": dsRecords,
		"Form":      gin.H{},
	})
}

// parseIP returns the canonical form of an IPv4 or IPv6 address.
func parseIP(value string) (string, bool) {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return "", false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String(), true
	}
	return ip.String(), true
}

func (a *Admin) createHost(c *gin.Context) {
	hostname := strings.ToLower(strings.TrimSpace(c.PostForm("hostname")))
	form := gin.H{"Hostname": hostname, "IP": c.PostForm("ip"), "Persistent": c.PostForm("persistent") != ""}

	fail := func(message string) {
		hosts, _ := a.store.ListHosts()
		var views []*hostView
		for _, host := range hosts {
			views = append(views, a.view(host))
		}
		a.render(c, http.StatusBadRequest, "hosts.html", gin.H{
			"Title": "Hosts", "Hosts": views, "Total": len(hosts), "Form": form, "FormError": message,
		})
	}

	if !shared.IsValidHostname(hostname) {
		fail("A hostname has 1 to 63 letters, digits and hyphens, and does not start or end with a hyphen.")
		return
	}
	ip, ok := parseIP(c.PostForm("ip"))
	if !ok {
		fail("Please enter a valid IPv4 or IPv6 address.")
		return
	}
	if _, err := a.store.GetHost(hostname); err == nil {
		fail(hostname + a.config.Domain + " already exists.")
		return
	}

	host := &shared.Host{Hostname: hostname, Ip: ip, Persistent: form["Persistent"].(bool)}
	host.GenerateAndSetToken()
	host.Touch()
	if err := a.store.SetHost(host); err != nil {
		fail("Could not save the host: " + err.Error())
		return
	}

	c.Redirect(http.StatusSeeOther, "/admin/hosts/"+hostname+"?msg=created")
}

// loadHost reads the host named in the URL, or redirects to the list.
func (a *Admin) loadHost(c *gin.Context) *shared.Host {
	hostname := c.Param("hostname")
	if shared.IsValidHostname(hostname) {
		if host, err := a.store.GetHost(hostname); err == nil {
			return host
		}
	}

	c.Redirect(http.StatusSeeOther, "/admin?msg=not-found")
	return nil
}

func (a *Admin) showHost(c *gin.Context) {
	host := a.loadHost(c)
	if host == nil {
		return
	}
	a.renderHost(c, http.StatusOK, host, "")
}

func (a *Admin) renderHost(c *gin.Context, status int, host *shared.Host, formError string) {
	base := a.config.PublicURL + "/update/" + host.Hostname
	updateURL := base + "/" + host.Token
	updateCommand := fmt.Sprintf(`curl -H "Authorization: Bearer %s" "%s"`, host.Token, base)

	a.render(c, status, "host.html", gin.H{
		"Title":         host.Hostname,
		"Host":          a.view(host),
		"UpdateURL":     updateURL,
		"UpdateCommand": updateCommand,
		"FormError":     formError,
	})
}

func (a *Admin) updateHost(c *gin.Context) {
	host := a.loadHost(c)
	if host == nil {
		return
	}

	ip, ok := parseIP(c.PostForm("ip"))
	if !ok {
		a.renderHost(c, http.StatusBadRequest, host, "Please enter a valid IPv4 or IPv6 address.")
		return
	}

	host.Ip = ip
	host.Persistent = c.PostForm("persistent") != ""
	host.Touch()
	if err := a.store.SetHost(host); err != nil {
		a.renderHost(c, http.StatusInternalServerError, host, "Could not save the host: "+err.Error())
		return
	}

	c.Redirect(http.StatusSeeOther, "/admin/hosts/"+host.Hostname+"?msg=updated")
}

func (a *Admin) regenerateToken(c *gin.Context) {
	host := a.loadHost(c)
	if host == nil {
		return
	}

	host.GenerateAndSetToken()
	if err := a.store.SetHost(host); err != nil {
		a.renderHost(c, http.StatusInternalServerError, host, "Could not save the host: "+err.Error())
		return
	}

	c.Redirect(http.StatusSeeOther, "/admin/hosts/"+host.Hostname+"?msg=token")
}

func (a *Admin) deleteHost(c *gin.Context) {
	host := a.loadHost(c)
	if host == nil {
		return
	}

	if err := a.store.DeleteHost(host.Hostname); err != nil {
		a.renderHost(c, http.StatusInternalServerError, host, "Could not delete the host: "+err.Error())
		return
	}

	c.Redirect(http.StatusSeeOther, "/admin?msg=deleted")
}
