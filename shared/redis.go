package shared

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/gomodule/redigo/redis"
)

type RedisBackend struct {
	expirationSeconds int
	pool              *redis.Pool
}

func NewRedisBackend(config *Config) *RedisBackend {
	return &RedisBackend{
		expirationSeconds: config.HostExpirationDays * 24 * 60 * 60,
		pool: &redis.Pool{
			MaxIdle:     3,
			IdleTimeout: 240 * time.Second,

			Dial: func() (redis.Conn, error) {
				c, err := redis.Dial("tcp", config.RedisHost)
				if err != nil {
					return nil, err
				}
				return c, err
			},

			TestOnBorrow: func(c redis.Conn, t time.Time) error {
				_, err := c.Do("PING")
				return err
			},
		},
	}
}

func (r *RedisBackend) Close() {
	r.pool.Close()
}

func (r *RedisBackend) GetHost(name string) (*Host, error) {
	conn := r.pool.Get()
	defer conn.Close()

	host := Host{Hostname: name}

	var err error
	var data []interface{}

	if data, err = redis.Values(conn.Do("HGETALL", host.Hostname)); err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, errors.New("Host does not exist")
	}

	if err = redis.ScanStruct(data, &host); err != nil {
		return nil, err
	}

	ttl, err := redis.Int64(conn.Do("PTTL", host.Hostname))
	if err != nil {
		return nil, err
	}
	if ttl > 0 {
		host.ExpiresIn = time.Duration(ttl) * time.Millisecond
	}

	return &host, nil
}

// ListHosts returns all registered hosts, sorted by name.
func (r *RedisBackend) ListHosts() ([]*Host, error) {
	conn := r.pool.Get()
	defer conn.Close()

	var names []string
	cursor := 0
	for {
		values, err := redis.Values(conn.Do("SCAN", cursor, "COUNT", 1000))
		if err != nil {
			return nil, err
		}

		keys, err := redis.Strings(values[1], nil)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			// other keys, like the DNSSEC ones, never look like a hostname
			if IsValidHostname(key) {
				names = append(names, key)
			}
		}

		if cursor, err = redis.Int(values[0], nil); err != nil {
			return nil, err
		}
		if cursor == 0 {
			break
		}
	}
	sort.Strings(names)

	hosts := []*Host{}
	for _, name := range names {
		host, err := r.GetHost(name)
		if err != nil {
			// expired since the scan
			continue
		}
		hosts = append(hosts, host)
	}

	return hosts, nil
}

func (r *RedisBackend) DeleteHost(name string) error {
	conn := r.pool.Get()
	defer conn.Close()

	_, err := conn.Do("DEL", name)
	return err
}

func (r *RedisBackend) SetHost(host *Host) error {
	conn := r.pool.Get()
	defer conn.Close()

	conn.Send("MULTI")
	r.sendHost(conn, host)
	return execOK(conn)
}

// CreateHost stores a new host, or returns ErrHostExists if the name is
// taken, also when another request creates it at the same time.
func (r *RedisBackend) CreateHost(host *Host) error {
	conn := r.pool.Get()
	defer conn.Close()

	if _, err := conn.Do("WATCH", host.Hostname); err != nil {
		return err
	}
	exists, err := redis.Bool(conn.Do("EXISTS", host.Hostname))
	if err != nil || exists {
		conn.Do("UNWATCH")
		if err == nil {
			err = ErrHostExists
		}
		return err
	}

	conn.Send("MULTI")
	r.sendHost(conn, host)
	if err := execOK(conn); err != nil {
		if errors.Is(err, redis.ErrNil) {
			// the watched key changed, so someone else registered it
			return ErrHostExists
		}
		return err
	}
	return nil
}

// sendHost queues the commands that store host and set its expiry.
func (r *RedisBackend) sendHost(conn redis.Conn, host *Host) {
	conn.Send("HMSET", redis.Args{}.Add(host.Hostname).AddFlat(host)...)
	if host.Persistent {
		conn.Send("PERSIST", host.Hostname)
	} else {
		conn.Send("EXPIRE", host.Hostname, r.expirationSeconds)
	}
}

// execOK runs EXEC and returns the first error, including those of single
// commands, which Redis reports in the reply instead of failing EXEC.
// It returns redis.ErrNil when a watched key changed and nothing ran.
func execOK(conn redis.Conn) error {
	replies, err := redis.Values(conn.Do("EXEC"))
	if err != nil {
		return err
	}
	for _, reply := range replies {
		if e, ok := reply.(redis.Error); ok {
			return e
		}
	}
	return nil
}

const (
	adminPasswordKey      = "admin:password-hash"
	adminSessionSecretKey = "admin:session-secret"
)

// AdminPasswordHash returns the bcrypt hash of the admin password, or an
// empty string when no password has been set.
func (r *RedisBackend) AdminPasswordHash() (string, error) {
	conn := r.pool.Get()
	defer conn.Close()

	hash, err := redis.String(conn.Do("GET", adminPasswordKey))
	if err == redis.ErrNil {
		return "", nil
	}
	return hash, err
}

// SetAdminPasswordHash stores a new admin password hash and rotates the
// session secret, which signs out every existing admin session.
func (r *RedisBackend) SetAdminPasswordHash(hash string) error {
	conn := r.pool.Get()
	defer conn.Close()

	if _, err := conn.Do("SET", adminPasswordKey, hash); err != nil {
		return err
	}
	_, err := conn.Do("DEL", adminSessionSecretKey)
	return err
}

// AdminSessionSecret returns the key admin session cookies are signed with,
// creating it on first use.
func (r *RedisBackend) AdminSessionSecret() ([]byte, error) {
	conn := r.pool.Get()
	defer conn.Close()

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if _, err := conn.Do("SET", adminSessionSecretKey, secret, "NX"); err != nil {
		return nil, err
	}

	return redis.Bytes(conn.Do("GET", adminSessionSecretKey))
}

func dnssecKeysKey(zone string) string     { return "dnssec:" + zone + ":keys" }
func dnssecNextIDKey(zone string) string   { return "dnssec:" + zone + ":next-key-id" }
func dnssecMetadataKey(zone string) string { return "dnssec:" + zone + ":metadata" }

func (r *RedisBackend) GetDomainKeys(zone string) ([]*DomainKey, error) {
	conn := r.pool.Get()
	defer conn.Close()

	values, err := redis.StringMap(conn.Do("HGETALL", dnssecKeysKey(zone)))
	if err != nil {
		return nil, err
	}

	keys := []*DomainKey{}
	for _, value := range values {
		key := &DomainKey{}
		if err := json.Unmarshal([]byte(value), key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })

	return keys, nil
}

func (r *RedisBackend) AddDomainKey(zone string, key *DomainKey) (int, error) {
	conn := r.pool.Get()
	defer conn.Close()

	id, err := redis.Int(conn.Do("INCR", dnssecNextIDKey(zone)))
	if err != nil {
		return -1, err
	}

	stored := *key
	stored.ID = id
	value, err := json.Marshal(&stored)
	if err != nil {
		return -1, err
	}

	if _, err := conn.Do("HSET", dnssecKeysKey(zone), id, value); err != nil {
		return -1, err
	}

	return id, nil
}

func (r *RedisBackend) RemoveDomainKey(zone string, id int) error {
	conn := r.pool.Get()
	defer conn.Close()

	_, err := conn.Do("HDEL", dnssecKeysKey(zone), id)
	return err
}

func (r *RedisBackend) UpdateDomainKey(zone string, id int, update func(*DomainKey)) error {
	conn := r.pool.Get()
	defer conn.Close()

	value, err := redis.Bytes(conn.Do("HGET", dnssecKeysKey(zone), id))
	if err != nil {
		return err
	}

	key := &DomainKey{}
	if err := json.Unmarshal(value, key); err != nil {
		return err
	}
	update(key)

	if value, err = json.Marshal(key); err != nil {
		return err
	}
	_, err = conn.Do("HSET", dnssecKeysKey(zone), id, value)
	return err
}

func (r *RedisBackend) GetAllDomainMetadata(zone string) (map[string][]string, error) {
	conn := r.pool.Get()
	defer conn.Close()

	values, err := redis.StringMap(conn.Do("HGETALL", dnssecMetadataKey(zone)))
	if err != nil {
		return nil, err
	}

	metadata := map[string][]string{}
	for kind, value := range values {
		var entries []string
		if err := json.Unmarshal([]byte(value), &entries); err != nil {
			return nil, err
		}
		metadata[kind] = entries
	}

	return metadata, nil
}

func (r *RedisBackend) SetDomainMetadata(zone, kind string, values []string) error {
	conn := r.pool.Get()
	defer conn.Close()

	if len(values) == 0 {
		_, err := conn.Do("HDEL", dnssecMetadataKey(zone), kind)
		return err
	}

	value, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = conn.Do("HSET", dnssecMetadataKey(zone), kind, value)
	return err
}
