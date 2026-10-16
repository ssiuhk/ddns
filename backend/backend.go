package backend

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pboehm/ddns/shared"
)

type Backend struct {
	config *shared.Config
	lookup *HostLookup
	keys   shared.KeyStore
	hosts  shared.HostManager
}

// Store is the storage the backend serves the zone and its keys from.
type Store interface {
	shared.KeyStore
	shared.HostManager
}

func NewBackend(config *shared.Config, lookup *HostLookup, store Store) *Backend {
	return &Backend{
		config: config,
		lookup: lookup,
		keys:   store,
		hosts:  store,
	}
}

// rpcCall is a request of the PowerDNS remote backend, sent by its HTTP
// connector with post=yes and post_json=yes.
type rpcCall struct {
	Method     string          `json:"method"`
	Parameters json.RawMessage `json:"parameters"`
}

type lookupParameters struct {
	QType      string `json:"qtype"`
	QName      string `json:"qname"`
	Remote     string `json:"remote"`
	Local      string `json:"local"`
	RealRemote string `json:"real-remote"`
	ZoneId     int    `json:"zone-id"`
}

type nameParameters struct {
	Name string `json:"name"`
}

func (b *Backend) Run() error {
	r := gin.New()
	r.Use(gin.Recovery())

	if b.config.Verbose {
		r.Use(gin.Logger())
	}

	// for the container health check; ?max_age=10m on a secondary also fails
	// when the data has not been synced for that long
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(shared.Health(b.hosts, shared.ParseMaxAge(c.Query("max_age"))))
	})

	r.POST("/dnsapi", func(c *gin.Context) {
		var call rpcCall
		if err := c.BindJSON(&call); err != nil {
			return
		}

		result, err := b.dispatch(&call)
		if err != nil {
			if b.config.Verbose {
				log.Printf("Error during %s: %v", call.Method, err)
			}
			result = false
		}

		c.JSON(200, gin.H{"result": result})
	})

	return r.Run(b.config.ListenBackend)
}

// dispatch answers a single remote backend call. A result of false tells
// PowerDNS that the call is unsupported or that there is nothing to return.
func (b *Backend) dispatch(call *rpcCall) (interface{}, error) {
	if result, handled, err := b.dispatchDNSSEC(call); handled {
		return result, err
	}

	switch call.Method {
	case "initialize":
		return true, nil

	case "lookup":
		var params lookupParameters
		if err := json.Unmarshal(call.Parameters, &params); err != nil {
			return nil, err
		}

		return b.lookup.Lookup(&Request{
			QType:      params.QType,
			QName:      params.QName,
			Remote:     params.Remote,
			Local:      params.Local,
			RealRemote: params.RealRemote,
			ZoneId:     params.ZoneId,
		})

	case "list":
		var params struct {
			ZoneName string `json:"zonename"`
		}
		if err := json.Unmarshal(call.Parameters, &params); err != nil {
			return nil, err
		}
		if !b.isOurZone(params.ZoneName) {
			return false, nil
		}
		return b.list()

	case "getAllDomains":
		return []interface{}{b.domainInfo()}, nil

	case "getDomainInfo":
		var params nameParameters
		if err := json.Unmarshal(call.Parameters, &params); err != nil {
			return nil, err
		}
		if !b.isOurZone(params.Name) {
			return false, nil
		}
		return b.domainInfo(), nil

	default:
		return false, nil
	}
}

func (b *Backend) domainInfo() gin.H {
	return gin.H{
		"id":     zoneID,
		"zone":   b.config.Zone() + ".",
		"kind":   "native",
		"serial": time.Now().Unix(),
	}
}

func (b *Backend) isOurZone(name string) bool {
	return shared.NormalizeName(name) == b.config.Zone()
}

// list returns every record of the zone, which pdnsutil uses to check it.
func (b *Backend) list() ([]*Response, error) {
	apex := b.config.Zone() + "."

	records, err := b.lookup.Lookup(&Request{QName: apex, QType: "ANY"})
	if err != nil {
		return nil, err
	}

	hosts, err := b.hosts.ListHosts()
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		hostRecords, err := b.lookup.Lookup(&Request{QName: host.Hostname + "." + apex, QType: "ANY"})
		if err == nil {
			records = append(records, hostRecords...)
		}
	}

	return records, nil
}
