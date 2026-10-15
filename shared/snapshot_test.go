package shared

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// testRedis returns a backend for the Redis in DDNS_TEST_REDIS, whose data
// the test replaces, or skips the test when it is not set.
func testRedis(t *testing.T) *RedisBackend {
	host := os.Getenv("DDNS_TEST_REDIS")
	if host == "" {
		t.Skip("set DDNS_TEST_REDIS to a scratch Redis to run this test")
	}
	r := NewRedisBackend(&Config{RedisHost: host, HostExpirationDays: 10})
	t.Cleanup(r.Close)
	return r
}

func TestDumpAndLoadRoundTrip(t *testing.T) {
	r := testRedis(t)

	assert.Nil(t, r.Load(&Snapshot{Version: snapshotVersion}))
	assert.Nil(t, r.SetHost(&Host{Hostname: "www", Ip: "10.0.0.1", Token: "abc"}))
	assert.Nil(t, r.SetHost(&Host{Hostname: "nas", Ip: "2001:db8::1", Token: "def", Persistent: true}))
	assert.Nil(t, r.SetAdminPasswordHash("hash"))
	_, err := r.AdminSessionSecret()
	assert.Nil(t, err)
	_, err = r.AddDomainKey("d.example.org", &DomainKey{Flags: 257, Active: true, Content: "key"})
	assert.Nil(t, err)

	snapshot, err := r.Dump()
	assert.Nil(t, err)

	var buffer bytes.Buffer
	assert.Nil(t, WriteSnapshot(&buffer, snapshot, true))

	// changes made after the dump are undone by loading it
	assert.Nil(t, r.DeleteHost("www"))
	assert.Nil(t, r.SetHost(&Host{Hostname: "extra", Ip: "10.0.0.9"}))

	read, err := ReadSnapshot(&buffer)
	assert.Nil(t, err)
	assert.Nil(t, r.Load(read))

	hosts, err := r.ListHosts()
	assert.Nil(t, err)
	var names []string
	for _, host := range hosts {
		names = append(names, host.Hostname)
	}
	assert.Equal(t, []string{"nas", "www"}, names)

	www, err := r.GetHost("www")
	assert.Nil(t, err)
	assert.True(t, www.ExpiresIn > 0)

	nas, err := r.GetHost("nas")
	assert.Nil(t, err)
	assert.True(t, nas.Persistent)
	assert.Zero(t, nas.ExpiresIn)

	keys, err := r.GetDomainKeys("d.example.org")
	assert.Nil(t, err)
	assert.Len(t, keys, 1)

	hash, err := r.AdminPasswordHash()
	assert.Nil(t, err)
	assert.Equal(t, "hash", hash)
}
