package frontend

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ssiuhk/ddns/shared"
)

// maxSyncSize limits the data a secondary accepts in one push.
const maxSyncSize = 64 << 20

// SyncStore is where a secondary keeps the data pushed by its primary.
type SyncStore interface {
	shared.Dumper
	Load(*shared.Snapshot) error
	LastLoad() (time.Time, error)
	ApplyPatch(*shared.Patch) error
	DumpKeys(keys []string) ([]shared.SnapshotItem, []string, error)
	Applied() (string, error)
	SetApplied(string) error
}

// ReplicaRouter is the HTTP service of a secondary: it accepts the data its
// primary pushes and reports its health, nothing else.
func ReplicaRouter(config *shared.Config, store SyncStore) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	if config.Verbose {
		r.Use(gin.Logger())
	}

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(shared.Health(store, shared.ParseMaxAge(c.Query("max_age"))))
	})

	sync := r.Group("", syncAuth(config.SyncToken))
	sync.POST(shared.SyncPath, func(c *gin.Context) { syncFull(c, store) })
	sync.POST(shared.SyncPatchPath, func(c *gin.Context) { syncPatch(c, store) })
	sync.POST(shared.SyncVerifyPath, func(c *gin.Context) { syncVerify(c, store) })
	return r
}

func syncAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.String(http.StatusNotFound, "Syncing is not set up on this server, set DDNS_SYNC_TOKEN\n")
			c.Abort()
			return
		}
		given, _ := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			address, _ := ExtractRemoteAddr(c.Request)
			log.Printf("Rejected a sync from %s: wrong token", address)
			c.String(http.StatusUnauthorized, "Wrong sync token\n")
			c.Abort()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSyncSize)
	}
}

// syncFull replaces all data with a full copy.
func syncFull(c *gin.Context, store SyncStore) {
	state := c.GetHeader(shared.SyncStateHeader)
	if state == "" {
		c.String(http.StatusBadRequest, "The primary did not say which state the data is of, update ddns there to the same version\n")
		return
	}
	snapshot, err := shared.ReadSnapshot(c.Request.Body, "")
	if err != nil {
		c.String(http.StatusBadRequest, "%v\n", err)
		return
	}
	if len(snapshot.Entries) == 0 {
		// a primary always has at least its admin and DNSSEC data, so this
		// is a primary reading the wrong Redis
		c.String(http.StatusBadRequest, "The data is empty, refusing to delete everything\n")
		return
	}

	if err := store.Load(snapshot); err != nil {
		log.Printf("Could not load the synced data: %v", err)
		c.String(http.StatusInternalServerError, "Could not load the data: %v\n", err)
		return
	}
	if err := store.SetApplied(state); err != nil {
		c.String(http.StatusInternalServerError, "%v\n", err)
		return
	}
	log.Printf("Loaded all %d keys from the primary", len(snapshot.Entries))
	syncVerify(c, store)
}

// syncPatch applies the changes since the state this server holds.
func syncPatch(c *gin.Context, store SyncStore) {
	body, err := gzip.NewReader(c.Request.Body)
	if err != nil {
		c.String(http.StatusBadRequest, "%v\n", err)
		return
	}
	patch := &shared.Patch{}
	if err := json.NewDecoder(body).Decode(patch); err != nil {
		c.String(http.StatusBadRequest, "%v\n", err)
		return
	}

	err = store.ApplyPatch(patch)
	if errors.Is(err, shared.ErrOutOfStep) {
		c.String(http.StatusConflict, "%v\n", err)
		return
	} else if err != nil {
		log.Printf("Could not apply the changes from the primary: %v", err)
		c.String(http.StatusInternalServerError, "Could not apply the changes: %v\n", err)
		return
	}

	// read back the changed keys, so the primary can check that every one
	// arrived and was stored as sent, and the deleted ones are gone
	keys := make([]string, 0, len(patch.Upserts)+len(patch.Deletes))
	for _, item := range patch.Upserts {
		keys = append(keys, item.Key)
	}
	stored, _, err := store.DumpKeys(keys)
	if err == nil && len(patch.Deletes) > 0 {
		var left []shared.SnapshotItem
		if left, _, err = store.DumpKeys(patch.Deletes); err == nil && len(left) > 0 {
			err = errors.New("deleted keys are still there")
		}
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "Applied the changes but could not read them back: %v\n", err)
		return
	}
	c.JSON(http.StatusOK, shared.SyncResult{Keys: len(stored), Fingerprint: shared.Fingerprint(stored), Applied: patch.To})
}

// syncVerify answers with a checksum of all data and the state it is of.
func syncVerify(c *gin.Context, store SyncStore) {
	applied, err := store.Applied()
	if err != nil {
		c.String(http.StatusInternalServerError, "%v\n", err)
		return
	}
	stored, err := store.Dump()
	if err != nil {
		c.String(http.StatusInternalServerError, "Could not read the data back: %v\n", err)
		return
	}
	c.JSON(http.StatusOK, shared.SyncResult{Keys: len(stored.Entries), Fingerprint: shared.Fingerprint(stored.Entries), Applied: applied})
}
