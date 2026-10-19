package shared

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// SyncPath is where a secondary accepts the data pushed by its primary.
const SyncPath = "/sync"

// Dumper is a store whose data can be pushed to secondaries.
type Dumper interface {
	Dump() (*Snapshot, error)
}

// SyncStatus is the state of pushing to one secondary.
type SyncStatus struct {
	LastSuccess time.Time `json:"last_success,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`

	pushed string // fingerprint of the data last pushed
}

// Pusher keeps secondaries in sync: it checks the data every Poll and pushes
// it to every target when it changed, and at least every Heartbeat, so a
// secondary that restarted or missed a push catches up and its health check
// can tell a broken sync from one with nothing to do.
type Pusher struct {
	Store     Dumper
	Targets   []string
	Token     string
	Poll      time.Duration
	Heartbeat time.Duration
	Client    *http.Client

	mu     sync.Mutex
	status map[string]*SyncStatus
}

func NewPusher(store Dumper, targets []string, token string) *Pusher {
	return &Pusher{
		Store:     store,
		Targets:   targets,
		Token:     token,
		Poll:      5 * time.Second,
		Heartbeat: time.Minute,
		Client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Run pushes until ctx is done.
func (p *Pusher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.Poll)
	defer ticker.Stop()
	for {
		p.PushOnce(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// PushOnce pushes the current data to every target that needs it.
func (p *Pusher) PushOnce(ctx context.Context) {
	snapshot, err := p.Store.Dump()
	if err != nil {
		for _, target := range p.Targets {
			p.failed(target, fmt.Errorf("could not read the data from Redis: %v", err))
		}
		return
	}
	fingerprint := Fingerprint(snapshot)

	var body []byte
	for _, target := range p.Targets {
		state := p.state(target)
		if state.pushed == fingerprint && time.Since(state.LastSuccess) < p.Heartbeat {
			continue
		}
		if body == nil {
			var buffer bytes.Buffer
			if err := WriteSnapshot(&buffer, snapshot, true, ""); err != nil {
				p.failed(target, err)
				continue
			}
			body = buffer.Bytes()
		}
		if err := p.push(ctx, target, body); err != nil {
			p.failed(target, err)
			continue
		}
		p.succeeded(target, fingerprint, len(snapshot.Entries))
	}
}

func (p *Pusher) push(ctx context.Context, target string, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target+SyncPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+p.Token)
	request.Header.Set("Content-Type", "application/octet-stream")

	response, err := p.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("%s answered %s: %s", target, response.Status, bytes.TrimSpace(message))
	}
	return nil
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

func (p *Pusher) succeeded(target, fingerprint string, keys int) {
	p.state(target)
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.status[target]
	if state.LastError != "" {
		log.Printf("Syncing to %s works again", target)
	}
	if state.pushed != fingerprint {
		log.Printf("Synced %d keys to %s", keys, target)
	}
	state.pushed = fingerprint
	state.LastSuccess = time.Now().UTC()
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

// Fingerprint identifies the data of a snapshot, leaving out when it was
// taken and the remaining TTLs, which change all the time.
func Fingerprint(snapshot *Snapshot) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, item := range snapshot.Entries {
		item.TTL = 0
		encoder.Encode(item)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
