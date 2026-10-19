package frontend

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ssiuhk/ddns/shared"
	"github.com/stretchr/testify/assert"
)

const testSyncToken = "0123456789abcdef0123456789abcdef"

// syncStore is a secondary's store and a primary's at the same time.
type syncStore struct {
	mu       sync.Mutex
	snapshot *shared.Snapshot
	loads    int
	loaded   time.Time
	// lose drops the last key on loading, like a broken secondary
	lose bool
}

func (s *syncStore) Dump() (*shared.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *s.snapshot
	copied.Created = time.Now().UTC()
	return &copied, nil
}
func (s *syncStore) Load(snapshot *shared.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lose && len(snapshot.Entries) > 0 {
		copied := *snapshot
		copied.Entries = copied.Entries[:len(copied.Entries)-1]
		snapshot = &copied
	}
	s.snapshot = snapshot
	s.loads++
	s.loaded = snapshot.Created
	return nil
}
func (s *syncStore) LastLoad() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded, nil
}

func snapshotWith(hosts ...string) *shared.Snapshot {
	snapshot := &shared.Snapshot{Version: 1, Created: time.Now().UTC()}
	for _, host := range hosts {
		snapshot.Entries = append(snapshot.Entries, shared.SnapshotItem{Key: host, Hash: map[string]string{"ip4": "192.0.2.1"}})
	}
	return snapshot
}

func newSecondary(t *testing.T) (*syncStore, *httptest.Server) {
	gin.SetMode(gin.TestMode)
	store := &syncStore{}
	server := httptest.NewServer(ReplicaRouter(&shared.Config{SyncToken: testSyncToken}, store))
	t.Cleanup(server.Close)
	return store, server
}

func postSync(t *testing.T, url, token string, snapshot *shared.Snapshot) (int, string) {
	var body bytes.Buffer
	assert.Nil(t, shared.WriteSnapshot(&body, snapshot, true, ""))
	request, _ := http.NewRequest("POST", url+shared.SyncPath, &body)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if !assert.Nil(t, err) {
		return 0, ""
	}
	defer response.Body.Close()
	var text bytes.Buffer
	text.ReadFrom(response.Body)
	return response.StatusCode, text.String()
}

func TestSyncEndpoint(t *testing.T) {
	store, server := newSecondary(t)

	code, _ := postSync(t, server.URL, "wrong", snapshotWith("pi"))
	assert.Equal(t, http.StatusUnauthorized, code)

	code, _ = postSync(t, server.URL, testSyncToken, snapshotWith())
	assert.Equal(t, http.StatusBadRequest, code)

	newer := snapshotWith("pi", "nas")
	code, text := postSync(t, server.URL, testSyncToken, newer)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, fmt.Sprintf(`{"keys":2,"fingerprint":"%s"}`, shared.Fingerprint(newer)), text)

	older := snapshotWith("old")
	older.Created = newer.Created.Add(-time.Minute)
	code, _ = postSync(t, server.URL, testSyncToken, older)
	assert.Equal(t, http.StatusConflict, code)
	assert.Len(t, store.snapshot.Entries, 2)
	assert.Equal(t, 1, store.loads)
}

func TestPusherPushesChangesAndHeartbeats(t *testing.T) {
	secondary, server := newSecondary(t)
	primary := &syncStore{snapshot: snapshotWith("pi")}

	pusher := shared.NewPusher(primary, []string{server.URL}, testSyncToken)
	pusher.Heartbeat = time.Hour
	ctx := context.Background()

	pusher.PushOnce(ctx)
	assert.Equal(t, 1, secondary.loads)

	// nothing changed, nothing to push
	pusher.PushOnce(ctx)
	assert.Equal(t, 1, secondary.loads)

	primary.Load(snapshotWith("pi", "nas"))
	pusher.PushOnce(ctx)
	assert.Equal(t, 2, secondary.loads)
	assert.Len(t, secondary.snapshot.Entries, 2)

	// after the heartbeat the same data is pushed again
	pusher.Heartbeat = 0
	pusher.PushOnce(ctx)
	assert.Equal(t, 3, secondary.loads)

	status := pusher.Status()[server.URL]
	assert.NotZero(t, status.LastSuccess)
	assert.Equal(t, 2, status.VerifiedKeys)
	assert.Empty(t, status.LastError)
}

func TestPusherNoticesASecondaryThatLostData(t *testing.T) {
	secondary, server := newSecondary(t)
	secondary.lose = true
	primary := &syncStore{snapshot: snapshotWith("pi", "nas")}

	pusher := shared.NewPusher(primary, []string{server.URL}, testSyncToken)
	pusher.Heartbeat = time.Hour
	pusher.PushOnce(context.Background())

	status := pusher.Status()[server.URL]
	assert.Zero(t, status.LastSuccess)
	assert.Contains(t, status.LastError, "stores 1 keys after loading that do not match the 2 sent")

	// not counted as synced, so the next check pushes again
	secondary.lose = false
	pusher.PushOnce(context.Background())
	assert.Equal(t, 2, secondary.loads)
	assert.Equal(t, 2, pusher.Status()[server.URL].VerifiedKeys)
}

func TestPusherReportsFailures(t *testing.T) {
	_, server := newSecondary(t)
	primary := &syncStore{snapshot: snapshotWith("pi")}

	pusher := shared.NewPusher(primary, []string{server.URL}, "wrong-token-wrong-token-wrong-token")
	pusher.PushOnce(context.Background())

	status := pusher.Status()[server.URL]
	assert.Zero(t, status.LastSuccess)
	assert.True(t, strings.Contains(status.LastError, "401"), status.LastError)
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
