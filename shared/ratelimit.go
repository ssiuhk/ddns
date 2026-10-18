package shared

import (
	"sync"
	"time"
)

// RateLimiter allows a limited number of events per key, like a client
// address, within a sliding time window.
type RateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	events map[string][]time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limit: limit, window: window, events: map[string][]time.Time{}}
}

// recent drops the events of key that are outside the window.
func (l *RateLimiter) recent(key string, now time.Time) []time.Time {
	var recent []time.Time
	for _, t := range l.events[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) == 0 {
		delete(l.events, key)
	} else {
		l.events[key] = recent
	}
	return recent
}

// Reserve records an event for key if it has fewer events than the limit
// in the window, and reports whether it did. Checking and recording in one
// step keeps concurrent requests from all seeing the same unused quota.
// A limit of zero or less allows everything.
func (l *RateLimiter) Reserve(key string) bool {
	if l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	// forget clients that have gone quiet, so the map does not grow forever
	if len(l.events) > 10000 {
		for other := range l.events {
			l.recent(other, now)
		}
	}

	recent := l.recent(key, now)
	if len(recent) >= l.limit {
		return false
	}
	l.events[key] = append(recent, now)
	return true
}

// Release gives back the most recent reservation of key, e.g. when the
// reserved operation failed.
func (l *RateLimiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if events := l.events[key]; len(events) > 0 {
		l.events[key] = events[:len(events)-1]
	}
}

// Reset forgets all events of key.
func (l *RateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.events, key)
}
