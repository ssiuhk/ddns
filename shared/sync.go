package shared

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// The paths where a secondary accepts what its primary pushes: a full copy
// of the data, the changes since the state it holds, and a request to
// confirm what it stores.
const (
	SyncPath       = "/sync"
	SyncPatchPath  = "/sync/patch"
	SyncVerifyPath = "/sync/verify"

	// SyncStateHeader carries the state of the primary a full copy is of.
	SyncStateHeader = "X-Sync-State"
)

// Dumper is a store whose data can be pushed to secondaries.
type Dumper interface {
	Dump() (*Snapshot, error)
}

// SyncSource is the store of a primary.
type SyncSource interface {
	Dumper
	SyncState() (SyncState, error)
	ChangedSince(seq, upTo int64) ([]string, error)
	DumpKeys(keys []string) ([]SnapshotItem, []string, error)
}

// SyncResult is a secondary's answer to a push: what it stores of the keys
// it was sent, read back from its Redis after writing them; for a full copy
// or a verification, that is all keys.
type SyncResult struct {
	Keys        int    `json:"keys"`
	Fingerprint string `json:"fingerprint"`
	// Applied is the state of the primary the secondary holds.
	Applied string `json:"applied,omitempty"`
}

// SyncStatus is the state of pushing to one secondary.
type SyncStatus struct {
	// LastSuccess is when the secondary last confirmed a push.
	LastSuccess time.Time `json:"last_success,omitzero"`
	// LastVerified is when it last confirmed that all its data matches the
	// primary's, and VerifiedKeys how many keys that was.
	LastVerified time.Time `json:"last_verified,omitzero"`
	VerifiedKeys int       `json:"verified_keys"`
	LastError    string    `json:"last_error,omitempty"`
	LastErrorAt  time.Time `json:"last_error_at,omitzero"`

	applied string // the state of the primary the secondary holds
	logged  string // the last state logged as synced
}

// Pusher keeps secondaries in sync. Every Poll it checks whether the data
// changed, and sends each secondary the keys changed since the state it
// holds. A secondary that is new, out of step, or further behind than the
// change log reaches gets a full copy. Every Heartbeat it sends an empty
// change, so a broken sync shows up, and every Verify both sides compare a
// checksum of all their data, and the secondary gets a full copy when it
// differs.
type Pusher struct {
	Store     SyncSource
	Targets   []string
	Token     string
	Poll      time.Duration
	Heartbeat time.Duration
	Verify    time.Duration
	Client    *http.Client

	mu     sync.Mutex
	status map[string]*SyncStatus
}

func NewPusher(store SyncSource, targets []string, token string) *Pusher {
	return &Pusher{
		Store:     store,
		Targets:   targets,
		Token:     token,
		Poll:      2 * time.Second,
		Heartbeat: time.Minute,
		Verify:    10 * time.Minute,
		Client:    &http.Client{Timeout: time.Minute},
	}
}

// Run pushes until ctx is done.
func (p *Pusher) Run(ctx context.Context) error {
	for {
		start := time.Now()
		p.PushOnce(ctx)

		// after a full copy or verification of a lot of data, leave Redis and
		// the CPU mostly idle for a while
		wait := max(p.Poll, 3*time.Since(start))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// PushOnce brings every target up to date.
func (p *Pusher) PushOnce(ctx context.Context) {
	for _, target := range p.Targets {
		if err := p.sync(ctx, target); err != nil {
			p.failed(target, err)
		}
	}
}

func (p *Pusher) sync(ctx context.Context, target string) error {
	state, err := p.Store.SyncState()
	if err != nil {
		return fmt.Errorf("could not read the change log from Redis: %v", err)
	}
	current := StateID(state.Epoch, state.Seq)
	status := p.state(target)

	epoch, seq := parseStateID(status.applied)
	if status.applied == "" || epoch != state.Epoch || seq < state.Floor {
		return p.pushFull(ctx, target)
	}

	if seq < state.Seq || time.Since(status.LastSuccess) >= p.Heartbeat {
		err := p.pushChanges(ctx, target, status.applied, seq, state)
		if errors.Is(err, ErrOutOfStep) {
			return p.pushFull(ctx, target)
		}
		if err != nil {
			return err
		}
		status = p.state(target)
	}

	if status.applied == current && time.Since(status.LastVerified) >= p.Verify {
		return p.verify(ctx, target, state)
	}
	return nil
}

// pushFull sends all data and has the secondary confirm all of it.
func (p *Pusher) pushFull(ctx context.Context, target string) error {
	// the state first: writes during the dump are in the dump and listed
	// again as changes after this state, which is harmless
	state, err := p.Store.SyncState()
	if err != nil {
		return err
	}
	snapshot, err := p.Store.Dump()
	if err != nil {
		return fmt.Errorf("could not read the data from Redis: %v", err)
	}
	var body bytes.Buffer
	if err := WriteSnapshot(&body, snapshot, true, ""); err != nil {
		return err
	}

	stateID := StateID(state.Epoch, state.Seq)
	result, err := p.post(ctx, target+SyncPath, &body, map[string]string{
		"Content-Type":  "application/octet-stream",
		SyncStateHeader: stateID,
	})
	if err != nil {
		return err
	}
	if result.Keys != len(snapshot.Entries) || result.Fingerprint != Fingerprint(snapshot.Entries) {
		return fmt.Errorf("%s stores %d keys after loading that do not match the %d sent", target, result.Keys, len(snapshot.Entries))
	}
	p.succeeded(target, stateID, len(snapshot.Entries), true)
	return nil
}

// pushChanges sends the keys changed since the state the secondary holds.
func (p *Pusher) pushChanges(ctx context.Context, target, base string, since int64, state SyncState) error {
	keys, err := p.Store.ChangedSince(since, state.Seq)
	if err != nil {
		return fmt.Errorf("could not read the change log from Redis: %v", err)
	}
	items, deleted, err := p.Store.DumpKeys(keys)
	if err != nil {
		return fmt.Errorf("could not read the data from Redis: %v", err)
	}
	patch := &Patch{
		Base:    base,
		To:      StateID(state.Epoch, state.Seq),
		Created: time.Now().UTC().Format(time.RFC3339),
		Upserts: items,
		Deletes: deleted,
	}
	body, err := gzipJSON(patch)
	if err != nil {
		return err
	}

	result, err := p.post(ctx, target+SyncPatchPath, body, map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip"})
	if err != nil {
		return err
	}
	if result.Keys != len(items) || result.Fingerprint != Fingerprint(items) {
		return fmt.Errorf("%s stores %d of the %d changed keys sent, differently", target, result.Keys, len(items))
	}
	p.succeeded(target, patch.To, len(items), false)
	return nil
}

// verify has the secondary confirm that all its data matches. The answer
// only counts when nothing changed on the primary meanwhile.
func (p *Pusher) verify(ctx context.Context, target string, before SyncState) error {
	snapshot, err := p.Store.Dump()
	if err != nil {
		return fmt.Errorf("could not read the data from Redis: %v", err)
	}
	result, err := p.post(ctx, target+SyncVerifyPath, nil, nil)
	if err != nil {
		return err
	}
	after, err := p.Store.SyncState()
	if err != nil {
		return err
	}
	current := StateID(before.Epoch, before.Seq)
	if after != before || result.Applied != current {
		return nil // something changed, try again later
	}

	if result.Keys != len(snapshot.Entries) || result.Fingerprint != Fingerprint(snapshot.Entries) {
		log.Printf("%s stores %d keys that do not match the %d of this server, sending a full copy", target, result.Keys, len(snapshot.Entries))
		p.mu.Lock()
		p.status[target].applied = ""
		p.mu.Unlock()
		return p.pushFull(ctx, target)
	}
	p.mu.Lock()
	p.status[target].LastVerified = time.Now().UTC()
	p.status[target].VerifiedKeys = len(snapshot.Entries)
	p.mu.Unlock()
	return nil
}

func (p *Pusher) post(ctx context.Context, url string, body io.Reader, headers map[string]string) (*SyncResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+p.Token)
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := p.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return nil, ErrOutOfStep
	}
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("%s answered %s: %s", url, response.Status, bytes.TrimSpace(message))
	}
	result := &SyncResult{}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(result); err != nil || result.Fingerprint == "" {
		return nil, fmt.Errorf("%s did not confirm what it stored, update ddns there to the same version", url)
	}
	return result, nil
}

func gzipJSON(value interface{}) (*bytes.Buffer, error) {
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	if err := json.NewEncoder(gz).Encode(value); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return &body, nil
}

func (p *Pusher) state(target string) *SyncStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status == nil {
		p.status = map[string]*SyncStatus{}
	}
	if p.status[target] == nil {
		p.status[target] = &SyncStatus{}
	}
	copied := *p.status[target]
	return &copied
}

func (p *Pusher) failed(target string, err error) {
	p.state(target)
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.status[target]
	// log when it starts failing or the reason changes, not on every retry
	if state.LastError != err.Error() {
		log.Printf("Could not sync to %s: %v", target, err)
	}
	state.LastError = err.Error()
	state.LastErrorAt = time.Now().UTC()
}

func (p *Pusher) succeeded(target, applied string, keys int, full bool) {
	p.state(target)
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.status[target]
	if state.LastError != "" {
		log.Printf("Syncing to %s works again", target)
	}
	now := time.Now().UTC()
	if full {
		log.Printf("Sent all %d keys to %s, verified", keys, target)
		state.LastVerified = now
		state.VerifiedKeys = keys
	} else if keys > 0 && state.logged != applied {
		log.Printf("Sent %d changed keys to %s, verified", keys, target)
	}
	state.logged = applied
	state.applied = applied
	state.LastSuccess = now
	state.LastError = ""
	state.LastErrorAt = time.Time{}
}

// Status returns the state of every target, for the health check.
func (p *Pusher) Status() map[string]SyncStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := map[string]SyncStatus{}
	for _, target := range p.Targets {
		if state := p.status[target]; state != nil {
			status[target] = *state
		} else {
			status[target] = SyncStatus{}
		}
	}
	return status
}

func parseStateID(id string) (string, int64) {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == ':' {
			var seq int64
			fmt.Sscan(id[i+1:], &seq)
			return id[:i], seq
		}
	}
	return "", 0
}

// Fingerprint identifies the data of snapshot entries, leaving out the
// remaining TTLs, which change all the time.
func Fingerprint(items []SnapshotItem) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, item := range items {
		item.TTL = 0
		encoder.Encode(item)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
