package main

import (
	"log"
	"os"

	"github.com/ssiuhk/ddns/admin"
	"github.com/ssiuhk/ddns/backend"
	"github.com/ssiuhk/ddns/frontend"
	"github.com/ssiuhk/ddns/shared"
	"golang.org/x/sync/errgroup"
)

var serviceConfig *shared.Config = &shared.Config{}

func init() {
	serviceConfig.Initialize()
}

func main() {
	commands := map[string]func([]string) int{
		"set-admin-password": setAdminPassword,
		"dump":               dumpData,
		"load":               loadData,
		"backup":             backupData,
		"dnssec":             dnssecCommand,
	}
	if len(os.Args) > 1 {
		if command, ok := commands[os.Args[1]]; ok {
			os.Exit(command(os.Args[2:]))
		}
	}

	serviceConfig.Validate()

	redis := shared.NewRedisBackend(serviceConfig)
	defer redis.Close()

	if serviceConfig.DNSSEC {
		setupDNSSEC(redis)
	}

	if serviceConfig.Replica {
		// a secondary only answers DNS from the data synced from the primary
		lookup := backend.NewHostLookup(serviceConfig, redis)
		log.Fatal(backend.NewBackend(serviceConfig, lookup, redis).Run())
	}

	var group errgroup.Group

	group.Go(func() error {
		lookup := backend.NewHostLookup(serviceConfig, redis)
		return backend.NewBackend(serviceConfig, lookup, redis).Run()
	})

	group.Go(func() error {
		adminPanel := admin.New(serviceConfig, redis, frontend.ExtractRemoteAddr)
		return frontend.NewFrontend(serviceConfig, redis, adminPanel).Run()
	})

	if err := group.Wait(); err != nil {
		log.Fatal(err)
	}
}

// setupDNSSEC creates a signing key for the zone if it has none yet and logs
// the DS records that have to be published in the parent zone.
func setupDNSSEC(keys shared.KeyStore) {
	zone := serviceConfig.Zone()

	// a secondary gets its keys from the primary, it must never create its own
	if !serviceConfig.Replica {
		created, err := shared.EnsureZoneIsSigned(keys, zone)
		if err != nil {
			log.Fatalf("Could not set up DNSSEC for %s: %v", zone, err)
		}
		if created {
			log.Printf("Created a DNSSEC signing key for %s", zone)
		}
	}

	records, err := shared.DSRecords(keys, zone)
	if err != nil {
		log.Fatalf("Could not read the DNSSEC keys of %s: %v", zone, err)
	}
	for _, ds := range records {
		log.Printf("DS record for the parent zone: %s", ds.String())
	}
	if len(records) == 0 && serviceConfig.Replica {
		log.Printf("No DNSSEC key for %s yet, sync the data from the primary before serving this zone", zone)
	}
}
