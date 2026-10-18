package main

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/ssiuhk/ddns/shared"
	"github.com/stretchr/testify/assert"
)

func TestCheckDSRecordsDuringRollover(t *testing.T) {
	zone := "d.example.org"
	ds := func(key *shared.DomainKey) *dns.DS {
		dnskey, err := key.PublicKey(zone)
		if err != nil {
			t.Fatal(err)
		}
		return dnskey.ToDS(dns.SHA256)
	}
	oldKey, err := shared.GenerateCSK(zone)
	assert.Nil(t, err)
	newKey, err := shared.GenerateCSK(zone)
	assert.Nil(t, err)
	plan := &shared.RolloverPlan{Step: shared.RolloverSwitch, Old: oldKey, New: newKey}

	// only the old DS: the new key is not trusted yet
	assert.NotNil(t, checkDSRecords([]*dns.DS{ds(oldKey)}, zone, plan))
	// both, as a parent following the CDS has them
	assert.Nil(t, checkDSRecords([]*dns.DS{ds(oldKey), ds(newKey)}, zone, plan))
	// the DS replaced by hand
	assert.Nil(t, checkDSRecords([]*dns.DS{ds(newKey)}, zone, plan))
}
