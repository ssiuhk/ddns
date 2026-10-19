package frontend

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gomodule/redigo/redis"
	"github.com/ssiuhk/ddns/shared"
	"github.com/stretchr/testify/assert"
)

const testSyncToken = "0123456789abcdef0123456789abcdef"

// countingStore counts the full copies a secondary loads.
type countingStore struct {
	*shared.RedisBackend
	loads int
}

func (s *countingStore) Load(snapshot *shared.Snapshot) error {
	s.loads++
	return s.RedisBackend.Load(snapshot)
}

// syncRedis returns an emptied primary and secondary on the two Redis in
// DDNS_TEST_SYNC_REDIS, or skips the test when it is not set.
func syncRedis(t *testing.T) (*shared.RedisBackend, *shared.RedisBackend) {
	hosts := strings.Split(os.Getenv("DDNS_TEST_SYNC_REDIS"), ",")
	if len(hosts) != 2 {
		t.Skip("set DDNS_TEST_SYNC_REDIS to two scratch Redis, host:port,host:port, to run this test")
	}
	var backends []*shared.RedisBackend
	for _, host := range hosts {
		conn, err := redis.Dial("tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Do("FLUSHDB")
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		backend := shared.NewRedisBackend(&shared.Config{RedisHost: host, HostExpirationDays: 10})
		t.Cleanup(backend.Close)
		backends = append(backends, backend)
	}
	return backends[0], backends[1]
}

func newSecondary(t *testing.T, backend *shared.RedisBackend) (*countingStore, *httptest.Server) {
	gin.SetMode(gin.TestMode)
	store := &countingStore{RedisBackend: backend}
	server := httptest.NewServer(ReplicaRouter(&shared.Config{SyncToken: testSyncToken}, store))
	t.Cleanup(server.Close)
	return store, server
}

func newPusher(primary *shared.RedisBackend, server *httptest.Server) *shared.Pusher {
	pusher := shared.NewPusher(primary, []string{server.URL}, testSyncToken)
	pusher.Heartbeat = time.Hour
	pusher.Verify = time.Hour
	return pusher
}

// assertSameData checks that both stores hold the same keys and values.
func assertSameData(t *testing.T, primary, secondary shared.Dumper) {
	t.Helper()
	want, err := primary.Dump()
	assert.Nil(t, err)
	got, err := secondary.Dump()
	assert.Nil(t, err)
	assert.Equal(t, len(want.Entries), len(got.Entries))
	assert.Equal(t, shared.Fingerprint(want.Entries), shared.Fingerprint(got.Entries))
}

func postFull(t *testing.T, url, token, state string, snapshot *shared.Snapshot) int {
	var body bytes.Buffer
	assert.Nil(t, shared.WriteSnapshot(&body, snapshot, true, ""))
	request, _ := http.NewRequest("POST", url+shared.SyncPath, &body)
	request.Header.Set("Authorization", "Bearer "+token)
	if state != "" {
		request.Header.Set(shared.SyncStateHeader, state)
	}
	response, err := http.DefaultClient.Do(request)
	if !assert.Nil(t, err) {
		return 0
	}
	response.Body.Close()
	return response.StatusCode
}

func TestSyncEndpointRejects(t *testing.T) {
	primary, backend := syncRedis(t)
	store, server := newSecondary(t, backend)
	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "pi", IPv4: "192.0.2.1", Token: "abc"}))
	snapshot, err := primary.Dump()
	assert.Nil(t, err)

	assert.Equal(t, http.StatusUnauthorized, postFull(t, server.URL, "wrong", "e:1", snapshot))
	assert.Equal(t, http.StatusBadRequest, postFull(t, server.URL, testSyncToken, "", snapshot))
	assert.Equal(t, http.StatusBadRequest, postFull(t, server.URL, testSyncToken, "e:1", &shared.Snapshot{Version: 1}))
	assert.Equal(t, 0, store.loads)

	assert.Equal(t, http.StatusOK, postFull(t, server.URL, testSyncToken, "e:1", snapshot))
	applied, err := store.Applied()
	assert.Nil(t, err)
	assert.Equal(t, "e:1", applied)
}

func TestPusherSendsOnlyChanges(t *testing.T) {
	primary, backend := syncRedis(t)
	secondary, server := newSecondary(t, backend)
	pusher := newPusher(primary, server)
	ctx := context.Background()

	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "pi", IPv4: "192.0.2.1", Token: "abc"}))
	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "old", IPv4: "192.0.2.2", Token: "def"}))
	assert.Nil(t, primary.SetAdminPasswordHash("hash"))

	// a new secondary gets a full copy
	pusher.PushOnce(ctx)
	assert.Equal(t, 1, secondary.loads)
	assertSameData(t, primary, secondary)

	// then only the changes, deletes included
	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "nas", IPv6: "2001:db8::1", Token: "ghi", Persistent: true}))
	_, err := primary.UpdateHost("pi", func(host *shared.Host) error { host.IPv4 = "192.0.2.9"; return nil })
	assert.Nil(t, err)
	assert.Nil(t, primary.DeleteHost("old"))
	_, err = primary.AddDomainKey("d.example.org", &shared.DomainKey{Flags: 257, Active: true, Content: "key"})
	assert.Nil(t, err)
	pusher.PushOnce(ctx)
	assert.Equal(t, 1, secondary.loads)
	assertSameData(t, primary, secondary)
	host, err := secondary.GetHost("pi")
	assert.Nil(t, err)
	assert.Equal(t, "192.0.2.9", host.IPv4)
	_, err = secondary.GetHost("old")
	assert.Equal(t, shared.ErrHostNotFound, err)

	state, err := primary.SyncState()
	assert.Nil(t, err)
	applied, err := secondary.Applied()
	assert.Nil(t, err)
	assert.Equal(t, shared.StateID(state.Epoch, state.Seq), applied)

	status := pusher.Status()[server.URL]
	assert.NotZero(t, status.LastSuccess)
	assert.Empty(t, status.LastError)

	// nothing changed, nothing sent until the heartbeat, which sends no data
	before := status.LastSuccess
	pusher.PushOnce(ctx)
	assert.Equal(t, before, pusher.Status()[server.URL].LastSuccess)
	pusher.Heartbeat = 0
	pusher.PushOnce(ctx)
	assert.True(t, pusher.Status()[server.URL].LastSuccess.After(before))
	assert.Equal(t, 1, secondary.loads)
}

func TestPusherSendsAFullCopyWhenOutOfStep(t *testing.T) {
	primary, backend := syncRedis(t)
	secondary, server := newSecondary(t, backend)
	pusher := newPusher(primary, server)
	ctx := context.Background()

	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "pi", IPv4: "192.0.2.1", Token: "abc"}))
	pusher.PushOnce(ctx)
	assert.Equal(t, 1, secondary.loads)

	// someone loaded a dump on the secondary: the changes no longer apply
	snapshot, err := secondary.Dump()
	assert.Nil(t, err)
	assert.Nil(t, secondary.RedisBackend.Load(snapshot))
	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "nas", IPv4: "192.0.2.2", Token: "def"}))
	pusher.PushOnce(ctx)
	assert.Equal(t, 2, secondary.loads)
	assertSameData(t, primary, secondary)
	assert.Empty(t, pusher.Status()[server.URL].LastError)

	// a dump loaded on the primary starts a new history
	snapshot, err = primary.Dump()
	assert.Nil(t, err)
	assert.Nil(t, primary.Load(snapshot))
	pusher.PushOnce(ctx)
	assert.Equal(t, 3, secondary.loads)
	assertSameData(t, primary, secondary)
}

func TestPusherVerifiesAllData(t *testing.T) {
	primary, backend := syncRedis(t)
	secondary, server := newSecondary(t, backend)
	pusher := newPusher(primary, server)
	ctx := context.Background()

	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "pi", IPv4: "192.0.2.1", Token: "abc"}))
	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "nas", IPv4: "192.0.2.2", Token: "def"}))
	pusher.PushOnce(ctx)
	assert.Equal(t, 1, secondary.loads)

	// the secondary loses a key behind the primary's back; the changes still
	// apply, but the verification notices and sends a full copy
	assert.Nil(t, secondary.DeleteHost("nas"))
	pusher.Verify = 0
	pusher.PushOnce(ctx)
	assert.Equal(t, 2, secondary.loads)
	assertSameData(t, primary, secondary)

	status := pusher.Status()[server.URL]
	assert.NotZero(t, status.LastVerified)
	assert.Equal(t, 2, status.VerifiedKeys)

	// and when all matches, it only records that
	pusher.PushOnce(ctx)
	assert.Equal(t, 2, secondary.loads)
	assert.True(t, pusher.Status()[server.URL].LastVerified.After(status.LastVerified))
}

func TestPusherReportsFailures(t *testing.T) {
	primary, backend := syncRedis(t)
	_, server := newSecondary(t, backend)
	assert.Nil(t, primary.SetHost(&shared.Host{Hostname: "pi", IPv4: "192.0.2.1", Token: "abc"}))

	pusher := newPusher(primary, server)
	pusher.Token = "wrong" + testSyncToken
	pusher.PushOnce(context.Background())

	status := pusher.Status()[server.URL]
	assert.Zero(t, status.LastSuccess)
	assert.Contains(t, status.LastError, "401")
}

func TestPrimaryHealthShowsSync(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := NewFrontend(&shared.Config{Domain: ".d.example.org", Registration: "open"}, memoryHosts{}, nil)
	f.SyncStatus = func() map[string]shared.SyncStatus {
		return map[string]shared.SyncStatus{"https://ns2.example.net": {LastSuccess: time.Now().Add(-10 * time.Minute)}}
	}
	r := f.Router()

	w, body := get(r, "/healthz")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, body, "sync")

	w, body = get(r, "/healthz?max_age=5m")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "stale", body["status"])
}
