package shared

import (
	"crypto"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
)

type memoryKeyStore struct {
	keys []*DomainKey
}

func (m *memoryKeyStore) GetDomainKeys(string) ([]*DomainKey, error) { return m.keys, nil }
func (m *memoryKeyStore) AddDomainKey(_ string, key *DomainKey) (int, error) {
	stored := *key
	stored.ID = 1
	if len(m.keys) > 0 {
		stored.ID = m.keys[len(m.keys)-1].ID + 1
	}
	m.keys = append(m.keys, &stored)
	return stored.ID, nil
}
func (m *memoryKeyStore) RemoveDomainKey(_ string, id int) error {
	for i, key := range m.keys {
		if key.ID == id {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
		}
	}
	return nil
}
func (m *memoryKeyStore) UpdateDomainKey(_ string, id int, update func(*DomainKey)) error {
	for _, key := range m.keys {
		if key.ID == id {
			update(key)
		}
	}
	return nil
}
func (m *memoryKeyStore) GetAllDomainMetadata(string) (map[string][]string, error) { return nil, nil }
func (m *memoryKeyStore) SetDomainMetadata(string, string, []string) error         { return nil }

func TestGeneratedKeyRoundTrips(t *testing.T) {
	key, err := GenerateCSK("d.example.org")
	assert.Nil(t, err)
	assert.Contains(t, key.Content, "Algorithm: 13 (ECDSAP256SHA256)")

	dnskey, err := key.PublicKey("d.example.org")
	assert.Nil(t, err)
	assert.Equal(t, uint16(257), dnskey.Flags)

	// a signature made with the stored private key verifies with the derived public key
	private, err := dnskey.ReadPrivateKey(strings.NewReader(key.Content), "test")
	assert.Nil(t, err)

	rr, _ := dns.NewRR("www.d.example.org. 5 IN A 10.0.0.1")
	sig := &dns.RRSIG{
		Hdr:        dns.RR_Header{Name: "www.d.example.org.", Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 5},
		Algorithm:  dnskey.Algorithm,
		KeyTag:     dnskey.KeyTag(),
		SignerName: dnskey.Hdr.Name,
		Inception:  1,
		Expiration: 4000000000,
	}
	assert.Nil(t, sig.Sign(private.(crypto.Signer), []dns.RR{rr}))
	assert.Nil(t, sig.Verify(dnskey, []dns.RR{rr}))
}

func TestEnsureZoneIsSignedOnlyOnce(t *testing.T) {
	store := &memoryKeyStore{}

	created, err := EnsureZoneIsSigned(store, "d.example.org")
	assert.Nil(t, err)
	assert.True(t, created)

	created, err = EnsureZoneIsSigned(store, "d.example.org")
	assert.Nil(t, err)
	assert.False(t, created)
	assert.Len(t, store.keys, 1)

	records, err := DSRecords(store, "d.example.org")
	assert.Nil(t, err)
	if assert.Len(t, records, 1) {
		assert.Equal(t, "d.example.org.", records[0].Hdr.Name)
		assert.Equal(t, uint8(dns.SHA256), records[0].DigestType)
	}
}
