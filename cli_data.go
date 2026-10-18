package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/ssiuhk/ddns/shared"
)

func redisFlag(flags *flag.FlagSet) *string {
	return flags.String("redis", shared.RedisHostDefault(),
		"The Redis socket that should be used (env DDNS_REDIS_HOST)")
}

func passphraseFlag(flags *flag.FlagSet) *string {
	return flags.String("passphrase-file", "",
		"File with the passphrase that encrypts dumps and backups (default env DDNS_BACKUP_PASSPHRASE or DDNS_BACKUP_PASSPHRASE_FILE)")
}

// dumpData writes all ddns data to stdout or a file, e.g. to pipe it to a
// secondary server, encrypted when a passphrase is configured:
//
//	docker compose exec -T ddns ddns dump > ddns.json
func dumpData(args []string) int {
	flags := flag.NewFlagSet("dump", flag.ExitOnError)
	redisHost := redisFlag(flags)
	passphraseFile := passphraseFlag(flags)
	output := flags.String("output", "-", "File to write to, - for stdout")
	compress := flags.Bool("gzip", false, "Compress the output with gzip")
	plain := flags.Bool("plain", false, "Do not encrypt the output even if a passphrase is configured")
	flags.Parse(args)

	passphrase, err := shared.LoadPassphrase(*passphraseFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *plain {
		passphrase = ""
	}

	redis := shared.NewRedisBackend(&shared.Config{RedisHost: *redisHost})
	defer redis.Close()

	snapshot, err := redis.Dump()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not read the data from Redis:", err)
		return 1
	}

	write := func(w io.Writer) error { return shared.WriteSnapshot(w, snapshot, *compress, passphrase) }
	if *output == "-" {
		err = write(os.Stdout)
	} else {
		// the dump holds tokens, the DNSSEC keys and the admin secrets
		err = shared.WritePrivateFile(*output, write)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not write the dump:", err)
		return 1
	}
	encrypted := ""
	if passphrase != "" {
		encrypted = ", encrypted"
	}
	fmt.Fprintf(os.Stderr, "Dumped %d keys%s\n", len(snapshot.Entries), encrypted)
	return 0
}

// loadData replaces all ddns data in Redis with a dump, plain, gzipped or
// encrypted, read from stdin or a file.
func loadData(args []string) int {
	flags := flag.NewFlagSet("load", flag.ExitOnError)
	redisHost := redisFlag(flags)
	passphraseFile := passphraseFlag(flags)
	input := flags.String("input", "-", "File to read from, - for stdin; a directory loads the newest backup in it")
	allowEmpty := flags.Bool("allow-empty", false, "Allow loading a dump without any data, which deletes everything")
	flags.Parse(args)

	var r io.Reader = os.Stdin
	if *input != "-" {
		path := *input
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if path, err = shared.LatestBackup(path); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			fmt.Fprintln(os.Stderr, "Loading", path)
		}

		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer file.Close()
		r = file
	}

	passphrase, err := shared.LoadPassphrase(*passphraseFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	snapshot, err := shared.ReadSnapshot(r, passphrase)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(snapshot.Entries) == 0 && !*allowEmpty {
		fmt.Fprintln(os.Stderr, "The dump is empty, refusing to delete all data (use --allow-empty to load it anyway)")
		return 1
	}

	redis := shared.NewRedisBackend(&shared.Config{RedisHost: *redisHost})
	defer redis.Close()

	if err := redis.Load(snapshot); err != nil {
		fmt.Fprintln(os.Stderr, "Could not load the data into Redis:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Loaded %d keys from a dump taken at %s\n",
		len(snapshot.Entries), snapshot.Created.Format(time.RFC3339))
	return 0
}

const backupRetry = time.Minute

// backupData takes a compressed backup into a directory every interval and
// deletes backups older than the retention. It runs as its own service.
func backupData(args []string) int {
	flags := flag.NewFlagSet("backup", flag.ExitOnError)
	redisHost := redisFlag(flags)
	passphraseFile := passphraseFlag(flags)
	dir := flags.String("dir", "/backups", "Directory the backups are written to")
	interval := flags.Duration("interval", 24*time.Hour, "Time between backups")
	keepDays := flags.Int("keep-days", 180, "Delete backups older than this many days")
	once := flags.Bool("once", false, "Take one backup and exit")
	flags.Parse(args)

	passphrase, err := shared.LoadPassphrase(*passphraseFile)
	if err != nil {
		log.Print(err)
		return 1
	}
	if passphrase == "" {
		log.Print("No backup passphrase is configured, backups are not encrypted and contain the DNSSEC private key")
	}

	redis := shared.NewRedisBackend(&shared.Config{RedisHost: *redisHost})
	defer redis.Close()

	run := func() error {
		snapshot, err := redis.Dump()
		if err != nil {
			return fmt.Errorf("could not read the data from Redis: %v", err)
		}

		now := time.Now()
		path, err := shared.WriteBackup(*dir, snapshot, now, passphrase)
		if err != nil {
			return fmt.Errorf("could not write the backup: %v", err)
		}
		log.Printf("Wrote backup %s with %d keys", path, len(snapshot.Entries))

		deleted, err := shared.PruneBackups(*dir, time.Duration(*keepDays)*24*time.Hour, now)
		for _, path := range deleted {
			log.Printf("Deleted backup %s, older than %d days", path, *keepDays)
		}
		return err
	}

	if *once {
		if err := run(); err != nil {
			log.Print(err)
			return 1
		}
		return 0
	}

	for {
		wait := *interval
		if err := run(); err != nil {
			// retry soon, e.g. while Redis is starting, instead of skipping a day
			log.Printf("%v, retrying in %s", err, backupRetry)
			wait = backupRetry
		}
		time.Sleep(wait)
	}
}
