package shared

import (
	"time"

	"github.com/gomodule/redigo/redis"
)

// lastLoadKey holds when the snapshot last loaded by `ddns load` was taken.
// It is not ddns data itself, so dumps leave it out.
const lastLoadKey = "sync:last-load"

// HealthChecker is a store that can report whether it works.
type HealthChecker interface {
	Ping() error
	// LastLoad is when the data last loaded from a dump was taken, zero if
	// none has been loaded.
	LastLoad() (time.Time, error)
}

func (r *RedisBackend) Ping() error {
	conn := r.pool.Get()
	defer conn.Close()

	_, err := conn.Do("PING")
	return err
}

func (r *RedisBackend) LastLoad() (time.Time, error) {
	conn := r.pool.Get()
	defer conn.Close()

	value, err := redis.String(conn.Do("GET", lastLoadKey))
	if err == redis.ErrNil {
		return time.Time{}, nil
	} else if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, value)
}

// Health reports the state of a ddns server as an HTTP status and body. On
// a secondary, maxDataAge > 0 also requires the last loaded data to be at
// most that old, so a broken sync shows up.
func Health(store interface{}, maxDataAge time.Duration) (int, map[string]interface{}) {
	checker, ok := store.(HealthChecker)
	if !ok {
		return 200, map[string]interface{}{"status": "ok"}
	}

	if err := checker.Ping(); err != nil {
		return 503, map[string]interface{}{"status": "error", "error": "redis: " + err.Error()}
	}

	body := map[string]interface{}{"status": "ok"}
	loaded, err := checker.LastLoad()
	if err != nil {
		return 503, map[string]interface{}{"status": "error", "error": "redis: " + err.Error()}
	}
	if !loaded.IsZero() {
		body["data_from"] = loaded.UTC().Format(time.RFC3339)
		body["data_age_seconds"] = int(time.Since(loaded).Seconds())
	}

	if maxDataAge > 0 && (loaded.IsZero() || time.Since(loaded) > maxDataAge) {
		body["status"] = "stale"
		return 503, body
	}
	return 200, body
}

// ParseMaxAge reads the max_age parameter of a health check, like 10m.
func ParseMaxAge(value string) time.Duration {
	age, err := time.ParseDuration(value)
	if err != nil || age < 0 {
		return 0
	}
	return age
}
