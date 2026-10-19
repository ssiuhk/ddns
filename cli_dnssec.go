package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/miekg/dns"
	"github.com/ssiuhk/ddns/shared"
)

const dnssecUsage = `Usage: ddns dnssec <command> [flags]

Commands:
  status     show the keys of the zone and their DS records
  rollover   do the next step of replacing the signing key; run it again
             when it says so, it finishes after three runs
`

// dnssecCommand manages the DNSSEC keys of the zone:
//
//	docker compose exec ddns ddns dnssec status
//	docker compose exec ddns ddns dnssec rollover
func dnssecCommand(args []string) int {
	if len(args) == 0 || (args[0] != "status" && args[0] != "rollover") {
		fmt.Fprint(os.Stderr, dnssecUsage)
		return 2
	}

	flags := flag.NewFlagSet("dnssec "+args[0], flag.ExitOnError)
	redisHost := redisFlag(flags)
	domain := flags.String("domain", os.Getenv("DDNS_DOMAIN"), "The zone, e.g. d.example.org (env DDNS_DOMAIN)")
	force := flags.Bool("force", false, "rollover: skip checking the DS records of the parent zone")
	flags.Parse(args[1:])

	zone := strings.Trim(shared.NormalizeName(*domain), ".")
	if zone == "" {
		fmt.Fprintln(os.Stderr, "Set the zone with --domain or DDNS_DOMAIN")
		return 2
	}

	redis := shared.NewRedisBackend(&shared.Config{RedisHost: *redisHost})
	defer redis.Close()

	if args[0] == "status" {
		return dnssecStatus(redis, zone)
	}
	return dnssecRollover(redis, zone, *force)
}

func describeKey(key *shared.DomainKey, zone string) (string, *dns.DS) {
	state := "inactive"
	if key.Active {
		state = "active (signing)"
	}
	if !key.Published {
		state += ", unpublished"
	}

	dnskey, err := key.PublicKey(zone)
	if err != nil {
		return fmt.Sprintf("key %d: unreadable: %v", key.ID, err), nil
	}
	return fmt.Sprintf("key %d: tag %d, algorithm %d, flags %d, %s",
		key.ID, dnskey.KeyTag(), dnskey.Algorithm, key.Flags, state), dnskey.ToDS(dns.SHA256)
}

func formatDS(ds *dns.DS) string {
	return strings.ReplaceAll(ds.String(), "\t", " ")
}

func dnssecStatus(store shared.KeyStore, zone string) int {
	keys, err := store.GetDomainKeys(zone)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not read the keys:", err)
		return 1
	}
	if len(keys) == 0 {
		fmt.Printf("%s has no DNSSEC key\n", zone)
		return 0
	}

	for _, key := range keys {
		description, ds := describeKey(key, zone)
		fmt.Println(description)
		if ds != nil {
			fmt.Println("  DS:", formatDS(ds))
		}
	}

	if plan, err := shared.PlanRollover(keys); err != nil {
		fmt.Println("\n" + err.Error())
	} else if plan.Step == shared.RolloverSwitch {
		fmt.Println("\nA rollover is in progress: once the parent zone only has the DS of the new key, run `ddns dnssec rollover`.")
	} else if plan.Step == shared.RolloverFinish {
		fmt.Println("\nA rollover is in progress: run `ddns dnssec rollover` once more to remove the old key.")
	}
	return 0
}

func dnssecRollover(store shared.KeyStore, zone string, force bool) int {
	keys, err := store.GetDomainKeys(zone)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not read the keys:", err)
		return 1
	}
	plan, err := shared.PlanRollover(keys)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if plan.Step == shared.RolloverSwitch && !force {
		if err := checkParentDS(zone, plan); err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "Run again once the parent zone is updated, or with --force if you are sure.")
			return 1
		}
	}

	if err := shared.ApplyRollover(store, zone, plan); err != nil {
		fmt.Fprintln(os.Stderr, "Could not change the keys:", err)
		return 1
	}

	switch plan.Step {
	case shared.RolloverStart:
		_, ds := describeKey(plan.New, zone)
		fmt.Printf(`Step 1 of 3: added key %d, which now signs %s alongside key %d.

Next:
  1. Wait at least an hour, so resolvers see the new key.
  2. Add this DS record for %s at your registrar, next to the old one or
     replacing it:
       %s
     A parent that reads CDS records adds it by itself, as the CDS now lists
     both keys.
  3. Wait until the parent's DS TTL has passed since the change, usually a day,
     then run `+"`ddns dnssec rollover`"+` again.
`, plan.New.ID, zone, plan.Old.ID, zone, formatDS(ds))
	case shared.RolloverSwitch:
		fmt.Printf(`Step 2 of 3: key %d no longer signs, key %d does.
Key %d stays published for signatures still cached. Wait at least an hour,
then run `+"`ddns dnssec rollover`"+` again to remove it.
`, plan.Old.ID, plan.New.ID, plan.Old.ID)
	case shared.RolloverFinish:
		fmt.Printf(`Step 3 of 3: removed key %d. The rollover to key %d is complete.
If the parent still has the DS record of key %d, remove it at your registrar;
a parent that reads CDS records removes it by itself.
`, plan.Old.ID, plan.New.ID, plan.Old.ID)
	}
	return 0
}

// checkParentDS makes sure resolvers see the DS of the new key before the
// old key stops signing. The DS of the old key may still be there: resolvers
// only need one DS that matches a signing key, and a parent following CDS
// removes it only once the old key has stopped signing.
func checkParentDS(zone string, plan *shared.RolloverPlan) error {
	records, err := lookupDS(zone)
	if err != nil {
		return fmt.Errorf("could not look up the DS records of %s: %v", zone, err)
	}
	return checkDSRecords(records, zone, plan)
}

func checkDSRecords(records []*dns.DS, zone string, plan *shared.RolloverPlan) error {
	if !hasDS(records, zone, plan.New) {
		return fmt.Errorf("the parent zone has no DS record for the new key %d yet", plan.New.ID)
	}
	return nil
}

// hasDS reports whether one of records is the DS of key.
func hasDS(records []*dns.DS, zone string, key *shared.DomainKey) bool {
	dnskey, err := key.PublicKey(zone)
	if err != nil {
		return false
	}
	for _, ds := range records {
		if ds.KeyTag == dnskey.KeyTag() && ds.Algorithm == dnskey.Algorithm {
			return true
		}
	}
	return false
}

func lookupDS(zone string) ([]*dns.DS, error) {
	config, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}

	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(zone), dns.TypeDS)
	client := new(dns.Client)

	err = fmt.Errorf("no nameserver in /etc/resolv.conf")
	for _, server := range config.Servers {
		var answer *dns.Msg
		answer, _, err = client.Exchange(query, net.JoinHostPort(server, config.Port))
		if err != nil {
			continue
		}
		if answer.Rcode != dns.RcodeSuccess {
			err = fmt.Errorf("the resolver answered %s", dns.RcodeToString[answer.Rcode])
			continue
		}

		var records []*dns.DS
		for _, rr := range answer.Answer {
			if ds, ok := rr.(*dns.DS); ok {
				records = append(records, ds)
			}
		}
		return records, nil
	}
	return nil, err
}
