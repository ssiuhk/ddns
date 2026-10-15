package shared

import (
	"errors"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// DomainKey is a DNSSEC key of a zone, in the shape the PowerDNS remote
// backend expects. Content holds the private key in BIND private key format.
type DomainKey struct {
	ID        int    `json:"id"`
	Flags     int    `json:"flags"`
	Active    bool   `json:"active"`
	Published bool   `json:"published"`
	Content   string `json:"content"`
}

// KeyStore persists DNSSEC keys and zone metadata.
type KeyStore interface {
	GetDomainKeys(zone string) ([]*DomainKey, error)
	AddDomainKey(zone string, key *DomainKey) (int, error)
	RemoveDomainKey(zone string, id int) error
	UpdateDomainKey(zone string, id int, update func(*DomainKey)) error

	GetAllDomainMetadata(zone string) (map[string][]string, error)
	SetDomainMetadata(zone, kind string, values []string) error
}

const (
	// FlagsCSK marks a combined signing key (KSK and ZSK in one), which is
	// also what `pdnsutil secure-zone` creates by default.
	FlagsCSK = 257
)

// GenerateCSK creates an ECDSA P-256 (algorithm 13) combined signing key.
func GenerateCSK(zone string) (*DomainKey, error) {
	dnskey := &dns.DNSKEY{
		Hdr:       dns.RR_Header{Name: dns.Fqdn(zone), Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 3600},
		Flags:     FlagsCSK,
		Protocol:  3,
		Algorithm: dns.ECDSAP256SHA256,
	}

	private, err := dnskey.Generate(256)
	if err != nil {
		return nil, err
	}

	return &DomainKey{
		Flags:     FlagsCSK,
		Active:    true,
		Published: true,
		Content:   dnskey.PrivateKeyString(private),
	}, nil
}

// EnsureZoneIsSigned creates a key for the zone unless it already has one.
func EnsureZoneIsSigned(store KeyStore, zone string) (bool, error) {
	keys, err := store.GetDomainKeys(zone)
	if err != nil {
		return false, err
	}
	if len(keys) > 0 {
		return false, nil
	}

	key, err := GenerateCSK(zone)
	if err != nil {
		return false, err
	}

	_, err = store.AddDomainKey(zone, key)
	return err == nil, err
}

// PublicKey derives the DNSKEY record of a key from its private key content.
func (k *DomainKey) PublicKey(zone string) (*dns.DNSKEY, error) {
	fields := parsePrivateKey(k.Content)

	algorithm, err := strconv.ParseUint(strings.Fields(fields["algorithm"] + " ")[0], 10, 8)
	if err != nil {
		return nil, errors.New("key content has no valid algorithm")
	}

	public, err := publicKeyFromPrivate(uint8(algorithm), fields)
	if err != nil {
		return nil, err
	}

	dnskey := &dns.DNSKEY{
		Hdr:       dns.RR_Header{Name: dns.Fqdn(zone), Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 3600},
		Flags:     uint16(k.Flags),
		Protocol:  3,
		Algorithm: uint8(algorithm),
	}
	if err := setPublicKey(dnskey, public); err != nil {
		return nil, err
	}

	return dnskey, nil
}

// DSRecords returns the DS records the parent zone needs for every active
// key signing key of the zone.
func DSRecords(store KeyStore, zone string) ([]*dns.DS, error) {
	keys, err := store.GetDomainKeys(zone)
	if err != nil {
		return nil, err
	}

	var records []*dns.DS
	for _, key := range keys {
		if !key.Active || !key.Published || key.Flags&dns.SEP == 0 {
			continue
		}

		dnskey, err := key.PublicKey(zone)
		if err != nil {
			return nil, err
		}
		records = append(records, dnskey.ToDS(dns.SHA256))
	}

	return records, nil
}

// parsePrivateKey reads the fields of a key in BIND private key format,
// which is how PowerDNS stores keys, with lowercased field names.
func parsePrivateKey(content string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		name, value, found := strings.Cut(line, ":")
		if found {
			fields[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
		}
	}
	return fields
}
