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
	for start := 0; start < len(keys); start += dumpBatch {
		batch := keys[start:min(start+dumpBatch, len(keys))]
		items, err := dumpBatchOf(conn, batch)
		if err != nil {
			return nil, err
		}
		snapshot.Entries = append(snapshot.Entries, items...)
	}

	return snapshot, nil
}

// dumpBatch is how many keys Dump reads per round trip; one round trip per
// key made dumping 100,000 hosts take seconds.
const dumpBatch = 1000

// dumpBatchOf reads keys with two pipelined round trips: their types, then
// their values and TTLs.
func dumpBatchOf(conn redis.Conn, keys []string) ([]SnapshotItem, error) {
	for _, key := range keys {
		conn.Send("TYPE", key)
	}
	if err := conn.Flush(); err != nil {
		return nil, err
	}
	kinds := make([]string, len(keys))
	for i, key := range keys {
		kind, err := redis.String(conn.Receive())
		if err != nil {
			return nil, err
		}
		switch kind {
		case "hash":
			conn.Send("HGETALL", key)
		case "string":
			conn.Send("GET", key)
		case "none":
			// expired while dumping
			continue
		default:
			return nil, fmt.Errorf("unexpected type %s of key %s", kind, key)
		}
		conn.Send("PTTL", key)
		kinds[i] = kind
	}
	if err := conn.Flush(); err != nil {
		return nil, err
	}

	// read every reply before returning, so the connection stays usable
	var items []SnapshotItem
	var firstErr error
	for i, key := range keys {
		if kinds[i] == "" {
			continue
		}
		item := SnapshotItem{Key: key}
		value, err := conn.Receive()
		ttl, ttlErr := redis.Int64(conn.Receive())
		if err == nil {
			err = ttlErr
		}
		if err == nil {
			if kinds[i] == "hash" {
				item.Hash, err = redis.StringMap(value, nil)
			} else {
				var raw []byte
				raw, err = redis.Bytes(value, nil)
				item.String = base64.StdEncoding.EncodeToString(raw)
			}
		}
		if errors.Is(err, redis.ErrNil) || ttl == -2 || (kinds[i] == "hash" && err == nil && len(item.Hash) == 0) {
			// expired between the two round trips
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("could not read key %s: %v", key, err)
			}
			continue
		}
		if ttl > 0 {
			item.TTL = ttl
		}
		items = append(items, item)
	}
	return items, firstErr
}

// Load replaces all ddns data in Redis with the snapshot in one transaction,
// so DNS answers never see a half loaded state.
func (r *RedisBackend) Load(snapshot *Snapshot) error {
	if snapshot.Version != snapshotVersion {
		return fmt.Errorf("unsupported snapshot version %d", snapshot.Version)
	}

	// validate everything before touching Redis, so a bad snapshot can not
	// fail half way through the transaction after the old data is deleted
	values := make([][]byte, len(snapshot.Entries))
	seen := make(map[string]bool, len(snapshot.Entries))
	for i, item := range snapshot.Entries {
		if !isDDNSKey(item.Key) {
			return fmt.Errorf("snapshot contains unexpected key %q", item.Key)
		}
		if seen[item.Key] {
			return fmt.Errorf("snapshot contains key %s more than once", item.Key)
		}
		seen[item.Key] = true
		if item.TTL < 0 {
			return fmt.Errorf("invalid ttl of key %s", item.Key)
		}
		if item.Hash != nil {
			if item.String != "" {
				return fmt.Errorf("key %s has both a hash and a string value", item.Key)
			}
			continue
		}
		if IsValidHostname(item.Key) {
			return fmt.Errorf("host %s is not a hash", item.Key)
		}
		value, err := base64.StdEncoding.DecodeString(item.String)
		if err != nil {
			return fmt.Errorf("invalid value of key %s: %v", item.Key, err)
		}
		values[i] = value
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

	// Do returns the first error of a queued command, which makes Redis
	// discard the whole transaction. Errors while executing are returned
	// per command and do not roll back the others, so check each of them.
	replies, err := redis.Values(conn.Do("EXEC"))
	if err != nil {
		return fmt.Errorf("the load was not applied: %v", err)
	}
	for _, reply := range replies {
		if e, ok := reply.(redis.Error); ok {
			return fmt.Errorf("the load failed part way, restore a backup: %v", e)
		}
	}
	return nil
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
