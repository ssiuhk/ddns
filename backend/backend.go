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
}

func NewBackend(config *shared.Config, lookup *HostLookup) *Backend {
	return &Backend{
		config: config,
		lookup: lookup,
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
