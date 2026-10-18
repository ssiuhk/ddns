package backend

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pboehm/ddns/shared"
)

// recordTTL is the TTL of every record we serve. It is short on purpose, as
// hosts are expected to change their address at any time.
const recordTTL = 5

// zoneID is the domain id we report to PowerDNS for the single zone we serve.
const zoneID = 1

type Request struct {
	QType      string
	QName      string
	Remote     string
	Local      string
	RealRemote string
	ZoneId     int
}

type Response struct {
	QType    string `json:"qtype"`
	QName    string `json:"qname"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	DomainID int    `json:"domain_id"`
	Auth     bool   `json:"auth"`
}

type HostLookup struct {
	config *shared.Config
	hosts  shared.HostBackend
}

func NewHostLookup(config *shared.Config, hostsBackend shared.HostBackend) *HostLookup {
	return &HostLookup{config, hostsBackend}
}

// Lookup returns every record matching the request. PowerDNS mostly asks for
// ANY and filters itself, so ANY returns the full RRset of the name.
func (l *HostLookup) Lookup(request *Request) ([]*Response, error) {
	queryName := shared.NormalizeName(request.QName)

	var records []*Response

	if queryName == l.config.Zone() {
		records = []*Response{
			l.record(request.QName, "SOA", fmt.Sprintf("%s. hostmaster.%s. %d 1800 3600 7200 5",
				l.config.SOAFqdn, l.config.Zone(), l.currentSOASerial())),
		}
		for _, ns := range l.nameservers() {
			records = append(records, l.record(request.QName, "NS", ns))
		}
	} else {
		hostname, err := l.extractHostname(queryName)
		if err != nil {
			return nil, err
		}

		host, err := l.hosts.GetHost(hostname)
		if err != nil {
			return nil, err
		}

		if host.IPv4 != "" {
			records = append(records, l.record(request.QName, "A", host.IPv4))
		}
		if host.IPv6 != "" {
			records = append(records, l.record(request.QName, "AAAA", host.IPv6))
		}
	}

	if request.QType == "ANY" {
		return records, nil
	}

	var matching []*Response
	for _, record := range records {
		if record.QType == request.QType {
			matching = append(matching, record)
		}
	}

	if len(matching) == 0 {
		return nil, errors.New("No record of the requested type")
	}

	return matching, nil
}

func (l *HostLookup) record(qname, qtype, content string) *Response {
	return &Response{QType: qtype, QName: qname, Content: content, TTL: recordTTL, DomainID: zoneID, Auth: true}
}

func (l *HostLookup) nameservers() []string {
	if len(l.config.Nameservers) > 0 {
		return l.config.Nameservers
	}
	return []string{l.config.SOAFqdn}
}

// extractHostname extract the host part of the fqdn: pi.d.example.org -> pi
func (l *HostLookup) extractHostname(queryName string) (string, error) {
	hostname := ""
	if strings.HasSuffix(queryName, l.config.Domain) {
		hostname = queryName[:len(queryName)-len(l.config.Domain)]
	}

	if hostname == "" || strings.Contains(hostname, ".") {
		return "", errors.New("Query name does not correspond to our domain")
	}

	return hostname, nil
}

// currentSOASerial get the current SOA serial by returning the current time in seconds
func (l *HostLookup) currentSOASerial() int64 {
	return time.Now().Unix()
}
