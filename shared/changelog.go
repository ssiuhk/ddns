package shared

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/gomodule/redigo/redis"
)

// The change log lets a primary send its secondaries only what changed.
// Every write of ddns data also records the keys it wrote in a sorted set,
// scored with a sequence number that grows with every write, so the keys
// changed since sequence n are those with a score above n. A key is listed
// once, with the score of its last change, so the log is never longer than
// the number of keys, plus deleted ones until it is trimmed.
//
// The epoch names one history of the data: loading a dump starts a new one,
// so secondaries get a full copy instead of changes that do not apply.
const (
	syncSeqKey     = "sync:seq"
	syncChangesKey = "sync:changes"
	syncFloorKey   = "sync:changes-floor"
	syncEpochKey   = "sync:epoch"
	// syncAppliedKey is on a secondary the "<epoch>:<seq>" it has applied.
	syncAppliedKey = "sync:applied"

	// maxChanges is how many keys the change log keeps; a secondary that
	// is further behind gets a full copy.
	maxChanges = 1000000
)

var recordScript = redis.NewScript(3, `
local seq = redis.call('INCR', KEYS[1])
for i, key in ipairs(ARGV) do
	redis.call('ZADD', KEYS[2], seq, key)
end
local excess = redis.call('ZCARD', KEYS[2]) - tonumber(`+fmt.Sprint(maxChanges)+`)
if excess > 0 then
	local cut = redis.call('ZRANGE', KEYS[2], excess - 1, excess - 1, 'WITHSCORES')
	redis.call('ZREMRANGEBYRANK', KEYS[2], 0, excess - 1)
	redis.call('SET', KEYS[3], cut[2])
end
return seq`)

// sendRecord queues recording keys as changed, for a MULTI. It sends the
// whole script, as a script missing from the cache would only fail at EXEC.
func sendRecord(conn redis.Conn, keys ...string) error {
	return recordScript.Send(conn, recordArgs(keys)...)
}

// doRecord records keys as changed right away.
func doRecord(conn redis.Conn, keys ...string) error {
	_, err := recordScript.Do(conn, recordArgs(keys)...)
	return err
}

func recordArgs(keys []string) []interface{} {
	args := []interface{}{syncSeqKey, syncChangesKey, syncFloorKey}
	for _, key := range keys {
		args = append(args, key)
	}
	return args
}

func newEpoch() string {
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	return hex.EncodeToString(id)
}

// SyncState is where the change log of a primary stands.
type SyncState struct {
	Epoch string
	Seq   int64
	// Floor is the highest sequence trimmed from the log: changes since
	// an older sequence are no longer all known.
	Floor int64
}

// SyncState returns where the change log stands, creating the epoch on
// first use.
func (r *RedisBackend) SyncState() (SyncState, error) {
	conn := r.pool.Get()
	defer conn.Close()

	if _, err := conn.Do("SET", syncEpochKey, newEpoch(), "NX"); err != nil {
		return SyncState{}, err
	}
	values, err := redis.Strings(conn.Do("MGET", syncEpochKey, syncSeqKey, syncFloorKey))
	if err != nil {
		return SyncState{}, err
	}
	state := SyncState{Epoch: values[0]}
	fmt.Sscan(values[1], &state.Seq)
	fmt.Sscan(values[2], &state.Floor)
	return state, nil
}

// ChangedSince returns the keys changed after sequence seq, up to upTo.
func (r *RedisBackend) ChangedSince(seq, upTo int64) ([]string, error) {
	conn := r.pool.Get()
	defer conn.Close()
	return redis.Strings(conn.Do("ZRANGEBYSCORE", syncChangesKey, fmt.Sprintf("(%d", seq), upTo))
}

// DumpKeys reads the given keys like Dump, and returns the ones that do
// not exist (any more) separately.
func (r *RedisBackend) DumpKeys(keys []string) ([]SnapshotItem, []string, error) {
	conn := r.pool.Get()
	defer conn.Close()

	var items []SnapshotItem
	for start := 0; start < len(keys); start += dumpBatch {
		batch, err := dumpBatchOf(conn, keys[start:min(start+dumpBatch, len(keys))])
		if err != nil {
			return nil, nil, err
		}
		items = append(items, batch...)
	}

	found := make(map[string]bool, len(items))
	for _, item := range items {
		found[item.Key] = true
	}
	var missing []string
	for _, key := range keys {
		if !found[key] {
			missing = append(missing, key)
		}
	}
	return items, missing, nil
}

// Applied returns the epoch and sequence a secondary has applied, empty if
// it has none, e.g. after a manual load.
func (r *RedisBackend) Applied() (string, error) {
	conn := r.pool.Get()
	defer conn.Close()

	applied, err := redis.String(conn.Do("GET", syncAppliedKey))
	if err == redis.ErrNil {
		return "", nil
	}
	return applied, err
}

// SetApplied records on a secondary which state of its primary it holds.
func (r *RedisBackend) SetApplied(applied string) error {
	conn := r.pool.Get()
	defer conn.Close()
	_, err := conn.Do("SET", syncAppliedKey, applied)
	return err
}

// ErrOutOfStep is returned when changes do not apply to what a secondary
// has, so it needs a full copy.
var ErrOutOfStep = fmt.Errorf("out of step with the primary, a full copy is needed")

// Patch is a set of changes a primary sends to bring a secondary from one
// state (Base) to the next (To), each written "<epoch>:<seq>".
type Patch struct {
	Base    string         `json:"base"`
	To      string         `json:"to"`
	Created string         `json:"created"`
	Upserts []SnapshotItem `json:"upserts,omitempty"`
	Deletes []string       `json:"deletes,omitempty"`
}

// ApplyPatch writes the changes of a patch in one transaction, if the
// secondary is at its base state, and returns ErrOutOfStep otherwise.
func (r *RedisBackend) ApplyPatch(patch *Patch) error {
	for _, item := range patch.Upserts {
		if !isDDNSKey(item.Key) {
			return fmt.Errorf("unexpected key %q", item.Key)
		}
	}
	for _, key := range patch.Deletes {
		if !isDDNSKey(key) {
			return fmt.Errorf("unexpected key %q", key)
		}
	}

	conn := r.pool.Get()
	defer conn.Close()

	if _, err := conn.Do("WATCH", syncAppliedKey); err != nil {
		return err
	}
	applied, err := redis.String(conn.Do("GET", syncAppliedKey))
	if err != nil && err != redis.ErrNil {
		conn.Do("UNWATCH")
		return err
	}
	if applied != patch.Base {
		conn.Do("UNWATCH")
		return ErrOutOfStep
	}

	conn.Send("MULTI")
	for _, item := range patch.Upserts {
		conn.Send("DEL", item.Key)
		if item.Hash != nil {
			if len(item.Hash) > 0 {
				conn.Send("HSET", redis.Args{}.Add(item.Key).AddFlat(item.Hash)...)
			}
		} else {
			value, err := decodeString(item)
			if err != nil {
				conn.Do("DISCARD")
				return err
			}
			conn.Send("SET", item.Key, value)
		}
		if item.TTL > 0 {
			conn.Send("PEXPIRE", item.Key, item.TTL)
		}
	}
	for _, key := range patch.Deletes {
		conn.Send("DEL", key)
	}
	conn.Send("SET", syncAppliedKey, patch.To)
	conn.Send("SET", lastLoadKey, patch.Created)

	if err := execOK(conn); err != nil {
		if err == redis.ErrNil {
			return ErrOutOfStep
		}
		return err
	}
	return nil
}

// StateID writes an epoch and sequence the way Patch and Applied use them.
func StateID(epoch string, seq int64) string {
	return fmt.Sprintf("%s:%d", epoch, seq)
}
