package backend

import (
	"encoding/json"

	"github.com/ssiuhk/ddns/shared"
)

// The remote backend cannot list the names of the zone in DNSSEC order, so
// denial of existence uses NSEC3 in narrow mode, where PowerDNS synthesizes
// the NSEC3 records on the fly.
const defaultNSEC3Param = "1 0 0 -"

// defaultMetadata is used for a kind unless it was set explicitly, e.g. with
// `pdnsutil set-meta`. PUBLISH-CDS lets parents that support RFC 8078 pick up
// the DS record automatically.
var defaultMetadata = map[string][]string{
	"NSEC3PARAM":  {defaultNSEC3Param},
	"PUBLISH-CDS": {"2"},
}

type keyParameters struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
	Key  struct {
		Flags     int    `json:"flags"`
		Active    bool   `json:"active"`
		Published bool   `json:"published"`
		Content   string `json:"content"`
	} `json:"key"`
}

type metadataParameters struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	Value []string `json:"value"`
}

// dispatchDNSSEC answers the DNSSEC related remote backend calls. The bool
// result reports whether the method is one of them.
func (b *Backend) dispatchDNSSEC(call *rpcCall) (interface{}, bool, error) {
	switch call.Method {
	case "getDomainKeys", "addDomainKey", "removeDomainKey",
		"activateDomainKey", "deactivateDomainKey", "publishDomainKey", "unpublishDomainKey":
		var params keyParameters
		if err := json.Unmarshal(call.Parameters, &params); err != nil {
			return nil, true, err
		}
		if !b.isOurZone(params.Name) {
			return false, true, nil
		}
		result, err := b.handleKeyCall(call.Method, &params)
		return result, true, err

	case "getAllDomainMetadata", "getDomainMetadata", "setDomainMetadata":
		var params metadataParameters
		if err := json.Unmarshal(call.Parameters, &params); err != nil {
			return nil, true, err
		}
		if !b.isOurZone(params.Name) {
			return false, true, nil
		}
		result, err := b.handleMetadataCall(call.Method, &params)
		return result, true, err
	}

	return nil, false, nil
}

func (b *Backend) handleKeyCall(method string, params *keyParameters) (interface{}, error) {
	zone := b.config.Zone()

	switch method {
	case "getDomainKeys":
		if !b.config.DNSSEC {
			return []interface{}{}, nil
		}
		return b.keys.GetDomainKeys(zone)

	case "addDomainKey":
		return b.keys.AddDomainKey(zone, &shared.DomainKey{
			Flags:     params.Key.Flags,
			Active:    params.Key.Active,
			Published: params.Key.Published,
			Content:   params.Key.Content,
		})

	case "removeDomainKey":
		return true, b.keys.RemoveDomainKey(zone, params.ID)
	}

	err := b.keys.UpdateDomainKey(zone, params.ID, func(key *shared.DomainKey) {
		switch method {
		case "activateDomainKey":
			key.Active = true
		case "deactivateDomainKey":
			key.Active = false
		case "publishDomainKey":
			key.Published = true
		case "unpublishDomainKey":
			key.Published = false
		}
	})
	return true, err
}

func (b *Backend) handleMetadataCall(method string, params *metadataParameters) (interface{}, error) {
	zone := b.config.Zone()

	if method == "setDomainMetadata" {
		if params.Kind == "NSEC3NARROW" {
			// narrow mode is required, see defaultNSEC3Param
			return true, nil
		}
		return true, b.keys.SetDomainMetadata(zone, params.Kind, params.Value)
	}

	metadata, err := b.keys.GetAllDomainMetadata(zone)
	if err != nil {
		return nil, err
	}

	for kind, values := range defaultMetadata {
		if _, ok := metadata[kind]; !ok {
			metadata[kind] = values
		}
	}
	metadata["NSEC3NARROW"] = []string{"1"}

	if method == "getAllDomainMetadata" {
		return metadata, nil
	}

	if values, ok := metadata[params.Kind]; ok {
		return values, nil
	}
	return []string{}, nil
}
