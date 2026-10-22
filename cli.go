package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ssiuhk/ddns/admin"
	"github.com/ssiuhk/ddns/shared"
	"golang.org/x/term"
)

const minPasswordLength = 10

// setAdminPassword sets or resets the admin password, which also signs out
// every admin session. It is meant to run inside the ddns container:
//
//	docker compose exec ddns ddns set-admin-password
func setAdminPassword(args []string) int {
	flags := flag.NewFlagSet("set-admin-password", flag.ExitOnError)
	redisHost := flags.String("redis", shared.RedisHostDefault(),
		"The Redis socket that should be used (env DDNS_REDIS_HOST)")
	passwordStdin := flags.Bool("password-stdin", false,
		"Read the password from the first line of stdin instead of prompting")
	flags.Parse(args)

	password, err := readNewPassword(*passwordStdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	hash, err := admin.HashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not hash the password:", err)
		return 1
	}

	redis := shared.NewRedisBackend(&shared.Config{RedisHost: *redisHost})
	defer redis.Close()

	if err := redis.SetAdminPasswordHash(hash); err != nil {
		fmt.Fprintln(os.Stderr, "Could not store the password:", err)
		return 1
	}

	fmt.Println("The admin password has been set. Existing admin sessions are signed out.")
	return 0
}

func readNewPassword(fromStdin bool) (string, error) {
	var password string

	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("could not read the password from stdin: %v", err)
		}
		password = strings.TrimRight(line, "\r\n")
	} else {
		fmt.Fprint(os.Stderr, "New admin password: ")
		first, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}

		fmt.Fprint(os.Stderr, "Repeat the password: ")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}

		if string(first) != string(second) {
			return "", fmt.Errorf("the passwords do not match")
		}
		password = string(first)
	}

	if len(password) < minPasswordLength {
		return "", fmt.Errorf("the password needs at least %d characters", minPasswordLength)
	}
	return password, nil
}
