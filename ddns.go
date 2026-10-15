package main

import (
	"github.com/pboehm/ddns/backend"
	"github.com/pboehm/ddns/frontend"
	"github.com/pboehm/ddns/shared"
	"golang.org/x/sync/errgroup"
	"log"
)

var serviceConfig *shared.Config = &shared.Config{}

func init() {
	serviceConfig.Initialize()
}

func main() {
	serviceConfig.Validate()

	redis := shared.NewRedisBackend(serviceConfig)
	defer redis.Close()

	if serviceConfig.DNSSEC {
		setupDNSSEC(redis)
	}

	var group errgroup.Group

	group.Go(func() error {
		lookup := backend.NewHostLookup(serviceConfig, redis)
		return backend.NewBackend(serviceConfig, lookup, redis).Run()
	})

	group.Go(func() error {
		return frontend.NewFrontend(serviceConfig, redis).Run()
	})

	if err := group.Wait(); err != nil {
		log.Fatal(err)
	}
}

// setupDNSSEC creates a signing key for the zone if it has none yet and logs
// the DS records that have to be published in the parent zone.
func setupDNSSEC(keys shared.KeyStore) {
	zone := serviceConfig.Zone()

	created, err := shared.EnsureZoneIsSigned(keys, zone)
	if err != nil {
		log.Fatalf("Could not set up DNSSEC for %s: %v", zone, err)
	}
	if created {
		log.Printf("Created a DNSSEC signing key for %s", zone)
	}

	records, err := shared.DSRecords(keys, zone)
	if err != nil {
		log.Fatalf("Could not read the DNSSEC keys of %s: %v", zone, err)
	}
	for _, ds := range records {
		log.Printf("DS record for the parent zone: %s", ds.String())
	}
}
