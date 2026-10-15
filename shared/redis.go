package shared

import (
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

	return &host, nil
}

func (r *RedisBackend) SetHost(host *Host) error {
	conn := r.pool.Get()
	defer conn.Close()

	var err error

	if _, err = conn.Do("HMSET", redis.Args{}.Add(host.Hostname).AddFlat(host)...); err != nil {
		return err
	}

	if _, err = conn.Do("EXPIRE", host.Hostname, r.expirationSeconds); err != nil {
		return err
	}

	return nil
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
