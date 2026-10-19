package frontend

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ssiuhk/ddns/shared"
)

// maxSyncSize limits the data a secondary accepts in one push.
const maxSyncSize = 64 << 20

// SyncStore is where a secondary loads the data pushed by its primary.
type SyncStore interface {
	Load(*shared.Snapshot) error
	LastLoad() (time.Time, error)
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
	r.POST(shared.SyncPath, syncHandler(config.SyncToken, store))
	return r
}

func syncHandler(token string, store SyncStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.String(http.StatusNotFound, "Syncing is not set up on this server, set DDNS_SYNC_TOKEN\n")
			return
		}
		given, _ := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			address, _ := ExtractRemoteAddr(c.Request)
			log.Printf("Rejected a sync from %s: wrong token", address)
			c.String(http.StatusUnauthorized, "Wrong sync token\n")
			return
		}

		body := http.MaxBytesReader(c.Writer, c.Request.Body, maxSyncSize)
		snapshot, err := shared.ReadSnapshot(body, "")
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

		loaded, err := store.LastLoad()
		if err != nil {
			c.String(http.StatusInternalServerError, "%v\n", err)
			return
		}
		if snapshot.Created.Before(loaded) {
			c.String(http.StatusConflict, "This server already has newer data, from %s\n", loaded.UTC().Format(time.RFC3339))
			return
		}

		if err := store.Load(snapshot); err != nil {
			log.Printf("Could not load the synced data: %v", err)
			c.String(http.StatusInternalServerError, "Could not load the data: %v\n", err)
			return
		}
		c.String(http.StatusOK, "Loaded %d keys\n", len(snapshot.Entries))
	}
}
