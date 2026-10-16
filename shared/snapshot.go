package shared

import (
	"bufio"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gomodule/redigo/redis"
)

// Snapshot is a portable copy of all ddns data in Redis: hosts, DNSSEC keys
// and metadata, and the admin credentials. It is used for backups and to
// sync a secondary server.
type Snapshot struct {
	Version int            `json:"version"`
	Created time.Time      `json:"created"`
	Entries []SnapshotItem `json:"entries"`
}

type SnapshotItem struct {
	Key string `json:"key"`
	// TTL is the remaining time to live in milliseconds, 0 for none
	TTL int64 `json:"ttl_ms,omitempty"`
	// Hash holds the fields of a Redis hash, String a base64 encoded string
	Hash   map[string]string `json:"hash,omitempty"`
	String string            `json:"string,omitempty"`
}

const snapshotVersion = 1

// isDDNSKey reports whether a Redis key holds ddns data.
func isDDNSKey(key string) bool {
	return IsValidHostname(key) || strings.HasPrefix(key, "dnssec:") || strings.HasPrefix(key, "admin:")
}

func (r *RedisBackend) ddnsKeys(conn redis.Conn) ([]string, error) {
	var keys []string
	cursor := 0
	for {
		values, err := redis.Values(conn.Do("SCAN", cursor, "COUNT", 1000))
		if err != nil {
			return nil, err
		}
		batch, err := redis.Strings(values[1], nil)
		if err != nil {
			return nil, err
		}
		for _, key := range batch {
			if isDDNSKey(key) {
				keys = append(keys, key)
			}
		}
		if cursor, err = redis.Int(values[0], nil); err != nil {
			return nil, err
		}
		if cursor == 0 {
			break
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Dump reads all ddns data from Redis.
func (r *RedisBackend) Dump() (*Snapshot, error) {
	conn := r.pool.Get()
	defer conn.Close()

	keys, err := r.ddnsKeys(conn)
	if err != nil {
		return nil, err
	}

	snapshot := &Snapshot{Version: snapshotVersion, Created: time.Now().UTC(), Entries: []SnapshotItem{}}
	for _, key := range keys {
		kind, err := redis.String(conn.Do("TYPE", key))
		if err != nil {
			return nil, err
		}

		item := SnapshotItem{Key: key}
		switch kind {
		case "hash":
			if item.Hash, err = redis.StringMap(conn.Do("HGETALL", key)); err != nil {
				return nil, err
			}
		case "string":
			value, err := redis.Bytes(conn.Do("GET", key))
			if err != nil {
				return nil, err
			}
			item.String = base64.StdEncoding.EncodeToString(value)
		case "none":
			// expired while dumping
			continue
		default:
			return nil, fmt.Errorf("unexpected type %s of key %s", kind, key)
		}

		ttl, err := redis.Int64(conn.Do("PTTL", key))
		if err != nil {
			return nil, err
		}
		if ttl == -2 {
			continue
		}
		if ttl > 0 {
			item.TTL = ttl
		}

		snapshot.Entries = append(snapshot.Entries, item)
	}

	return snapshot, nil
}

// Load replaces all ddns data in Redis with the snapshot in one transaction,
// so DNS answers never see a half loaded state.
func (r *RedisBackend) Load(snapshot *Snapshot) error {
	if snapshot.Version != snapshotVersion {
		return fmt.Errorf("unsupported snapshot version %d", snapshot.Version)
	}

	// validate everything before touching Redis
	values := make([][]byte, len(snapshot.Entries))
	for i, item := range snapshot.Entries {
		if !isDDNSKey(item.Key) {
			return fmt.Errorf("snapshot contains unexpected key %q", item.Key)
		}
		if item.Hash == nil {
			value, err := base64.StdEncoding.DecodeString(item.String)
			if err != nil {
				return fmt.Errorf("invalid value of key %s: %v", item.Key, err)
			}
			values[i] = value
		}
	}

	conn := r.pool.Get()
	defer conn.Close()

	existing, err := r.ddnsKeys(conn)
	if err != nil {
		return err
	}

	conn.Send("MULTI")
	for _, key := range existing {
		conn.Send("DEL", key)
	}
	for i, item := range snapshot.Entries {
		if item.Hash != nil {
			if len(item.Hash) == 0 {
				continue
			}
			conn.Send("HSET", redis.Args{}.Add(item.Key).AddFlat(item.Hash)...)
		} else {
			conn.Send("SET", item.Key, values[i])
		}

		if item.TTL > 0 {
			conn.Send("PEXPIRE", item.Key, item.TTL)
		}
	}
	conn.Send("SET", lastLoadKey, snapshot.Created.UTC().Format(time.RFC3339))

	_, err = conn.Do("EXEC")
	return err
}

// WriteSnapshot writes a snapshot as JSON, gzip compressed if compress is
// set, and encrypted with passphrase unless it is empty.
func WriteSnapshot(w io.Writer, snapshot *Snapshot, compress bool, passphrase string) error {
	if passphrase != "" {
		encrypted, err := encryptingWriter(w, passphrase)
		if err != nil {
			return err
		}
		if err := WriteSnapshot(encrypted, snapshot, compress, ""); err != nil {
			return err
		}
		return encrypted.Close()
	}

	if compress {
		gz := gzip.NewWriter(w)
		if err := json.NewEncoder(gz).Encode(snapshot); err != nil {
			return err
		}
		return gz.Close()
	}
	return json.NewEncoder(w).Encode(snapshot)
}

// ReadSnapshot reads a snapshot written by WriteSnapshot, compressed,
// encrypted or not. Encrypted data needs the passphrase.
func ReadSnapshot(r io.Reader, passphrase string) (*Snapshot, error) {
	plain, err := decryptingReader(bufio.NewReader(r), passphrase)
	if err != nil {
		return nil, err
	}
	buffered := bufio.NewReader(plain)

	var reader io.Reader = buffered
	if magic, err := buffered.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}

	snapshot := &Snapshot{}
	if err := json.NewDecoder(reader).Decode(snapshot); err != nil {
		return nil, fmt.Errorf("could not read the snapshot: %v", err)
	}
	if snapshot.Version == 0 {
		return nil, errors.New("could not read the snapshot: no version")
	}
	return snapshot, nil
}
