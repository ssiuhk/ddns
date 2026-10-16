package shared

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHostAddressesInRedis(t *testing.T) {
	r := testRedis(t)
	conn := r.pool.Get()
	defer conn.Close()

	// a host saved before IPv4 and IPv6 were kept apart
	_, err := conn.Do("HSET", "legacy", "ip", "2001:db8::5", "token", "abc", "persistent", "1")
	assert.Nil(t, err)
	t.Cleanup(func() { r.DeleteHost("legacy") })

	host, err := r.GetHost("legacy")
	if assert.Nil(t, err) {
		assert.Equal(t, "", host.IPv4)
		assert.Equal(t, "2001:db8::5", host.IPv6)
	}

	host.SetIP("192.0.2.5")
	assert.Nil(t, r.SetHost(host))

	fields, err := conn.Do("HGETALL", "legacy")
	assert.Nil(t, err)
	assert.NotContains(t, fields, []byte("ip"))

	host, err = r.GetHost("legacy")
	if assert.Nil(t, err) {
		assert.Equal(t, "192.0.2.5", host.IPv4)
		assert.Equal(t, "2001:db8::5", host.IPv6)
		assert.Equal(t, []string{"192.0.2.5", "2001:db8::5"}, host.Addresses())
	}
}
