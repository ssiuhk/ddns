# `ddns` - Dynamic DNS

[![CircleCI](https://img.shields.io/circleci/build/github/pboehm/ddns?style=flat)](https://circleci.com/github/pboehm/ddns)
[![Docker Image Size (tag)](https://img.shields.io/docker/image-size/pboehm/ddns/latest?logo=Docker)](https://hub.docker.com/r/pboehm/ddns)
[![Docker Pulls](https://img.shields.io/docker/pulls/pboehm/ddns?logo=Docker)](https://hub.docker.com/r/pboehm/ddns)
[![GitHub](https://img.shields.io/github/license/ssiuhk/ddns?style=flat)](https://github.com/ssiuhk/ddns)


A self-hosted Dynamic DNS solution similar to DynDNS or NO-IP based on the
[PowerDNS Remote Backend](https://doc.powerdns.com/md/authoritative/backend-remote/).

<img src="screenshot.png" alt="screenshot" width="500"/>


## ⚠️⚠️⚠️ Note regarding hosted version at `ddns.pboehm.de`

The hosted `ddns` service with custom hosts under the `d.pboehm.de` domain (e.g `test.d.pboehm.de`) is not available
anymore! I did not use this service for quite some time and recently there were some issues which resulted in downtime
and expiration of all registered hosts.

### Alternatives

There is at least one other hosted version of `pboehm/ddns` that
[can be found via Google](https://www.google.com/search?q=pboehm%2Fddns+%22DDNS+is+a+project+that+lets+you+host+a+Dynamic+DNS+Service%2C+similar+to+DynDNS%2FNO-IP%2C+on+your+own+servers.%22)
or you can host it yourself as described below.

## How can I update my IP if it changes?

`ddns` is built around a small webservice, so that you can update your IP address simply by calling
an URL periodically through `curl`. Hosts that haven't been updated for 10 days will
be automatically removed. This can be configured in your own instance.

Registering a host returns a token. Send it in a header, which keeps it out of access logs:

```
curl -H "Authorization: Bearer <token>" https://ddns.example.net/update/<hostname>
```

The older form with the token in the URL, `https://ddns.example.net/update/<hostname>/<token>`, keeps
working for existing clients. Tokens are 256 bit random values; hosts registered before this change keep their
old, weaker token until you generate a new one in the admin panel.

A host can have an IPv4 and an IPv6 address at the same time, served as its A and AAAA record. An update
sets the address of the family it comes from and keeps the other one. To set addresses explicitly, for example
both at once, add `?myip=203.0.113.10,2001:db8::1` to the update URL.

### Routers and ddclient (DynDNS2 protocol)

`ddns` also speaks the DynDNS2 update protocol used by routers (Fritz!Box, UniFi, pfSense, OPNsense) and
[ddclient](https://ddclient.net/). Configure a "custom" or "DynDNS2" provider with:

* Server / update URL: `https://ddns.example.net/nic/update?hostname=<domain>&myip=<ipaddr>,<ip6addr>`
  (the placeholders differ per router; leave out `myip` to use the address the request comes from)
* Username: the hostname, for example `myhost` or `myhost.d.example.net`
* Password: the token of the host

For ddclient:

```
protocol=dyndns2
server=ddns.example.net
ssl=yes
login=myhost
password=<token>
myhost.d.example.net
```

The answer is one line per host: `good <ip>`, `nochg <ip>`, `nohost`, `badauth` or `notfqdn`.

## Self-Hosting

### Requirements

* A custom domain where the registrar allows setting `NS` records for subdomains. This is important because not all
  DNS providers support this.
* A server with [docker](https://www.docker.com/) and the [docker compose](https://docs.docker.com/compose/) plugin installed
* The following ports should be opened in the firewall:
  * `53/udp`
  * `53/tcp`
  * `80/tcp`
  * `443/tcp`

### DNS-Setup

For the domain you want to use with `ddns` (`example.net` in the following sections, please adjust this to your domain)
you have to create the following two DNS records:

* `ddns.example.net` as a `CNAME` or `A`/`AAAA` record pointing to the server `ddns` will be running on. This record
  will be used for accessing the `ddns` frontend in your browser or via `curl`. It is also the target for the
  corresponding `NS` record.
* `d.example.net` as an `NS` record pointing to the previously created `ddns.example.net` record. This will delegate
  all subdomains under `d.example.net` to the PowerDNS server running on `ddns.example.net`.

### `ddns`-Setup

The setup is now automated using [docker-compose](https://docs.docker.com/compose/) and only some customization has
to be made in a `docker-compose.override.yml` file
(a [sample](./docker/docker-compose.override.yml.sample) is available here).

#### Configuring the Setup

The setup included in this repository contains all the components described above
([PowerDNS Authoritative Server](https://doc.powerdns.com/authoritative/) 5.1 with the remote backend, Redis and `ddns`)
and uses [caddy](https://caddyserver.com/) 2 as a web server, because it provides automatic HTTPS using Lets Encrypt.

```
git clone git@github.com:ssiuhk/ddns.git
cd ddns/docker
cp docker-compose.override.yml.sample docker-compose.override.yml
```

Please adjust the settings in `docker-compose.override.yml` marked with the `#<<< ....` comments as follows:

* adjust the domain part in lines marked with `# <<< ADJUST DOMAIN` according to your DNS-Setup
* insert your email address in lines marked with `# <<< INSERT EMAIL` which is required for getting certificates
  from Lets Encrypt

By default anyone can register a host on the start page, at most 5 per address and hour. Set
`DDNS_REGISTRATION: closed` to only create hosts in the [admin panel](#admin-panel), or change the limit with
`DDNS_REGISTRATIONS_PER_HOUR` (0 for no limit).

Finally execute the following `docker compose` command, which creates 4 containers in detached mode which are also
started automatically after reboot. For updating an existing installation use the same command because it automatically
rebuilds the containers.

```
docker compose --project-name ddns up -d --build
```

### DNSSEC

The zone is signed with DNSSEC by default. On its first start `ddns` creates an ECDSA P-256 (algorithm 13) signing
key for the zone, stores it in Redis next to the hosts, and logs the DS record the parent zone needs:

```
docker compose --project-name ddns logs ddns | grep "DS record"
```

PowerDNS signs every answer on the fly and uses NSEC3 in narrow mode for denial of existence, as hosts come and go
at any time. To complete the chain of trust, add the logged DS record for `d.example.net` to the parent zone
`example.net` at your DNS provider. Providers that support [RFC 8078](https://www.rfc-editor.org/rfc/rfc8078) can
pick it up automatically from the published CDS record. Until the DS record is in place, resolvers treat the zone as
unsigned, so nothing breaks in the meantime.

Make sure Redis data is persisted (the `.redis_mount` volume of the sample setup), as losing the key means the DS
record in the parent zone no longer matches and validating resolvers will fail to resolve your hosts.

`ddns dnssec status` shows the keys and their DS records. To replace the signing key, for example once a year or
if it may have leaked, run the rollover on the primary:

```
docker compose --project-name ddns exec ddns ddns dnssec rollover
```

Each run does one step and says what to do before the next one:

1. A new key is added and signs next to the old one. After an hour, replace the DS record at your registrar with the
   new one it prints.
2. Once the parent's DS TTL (usually a day) has passed, the next run checks that resolvers see the new DS record and
   not the old one, then lets the old key stop signing.
3. An hour later, the last run removes the old key.

On a setup with a secondary, the sync carries every step over to it within a minute.

DNSSEC can be turned off with `DDNS_DNSSEC: "false"` in the `ddns` environment. Remove the DS record from the parent
zone first, or validating resolvers will reject the then unsigned answers.

### Admin panel

`ddns` has a control panel for a single administrator at `https://ddns.example.net/admin`, where you can list, show,
create, update and delete hosts, mark hosts that should never expire, generate new update tokens, and see the DS
record of the zone.

#### Password

Set the admin password from the running container. The same command resets it, which also signs out every open admin
session:

```
docker compose --project-name ddns exec ddns ddns set-admin-password
```

It asks for the password twice (at least 10 characters). To set it from a script, pipe it in with
`--password-stdin`:

```
echo "$PASSWORD" | docker compose --project-name ddns exec -T ddns ddns set-admin-password --password-stdin
```

Only a bcrypt hash of the password is stored, in Redis. After 10 failed attempts from one address, logins from it are
refused for 15 minutes.

#### Sign in with Google (optional)

1. In the [Google Cloud console](https://console.cloud.google.com/apis/credentials) create an OAuth client ID of type
   "Web application" with the authorized redirect URI `https://ddns.example.net/admin/login/google/callback`.
2. Set `DDNS_GOOGLE_CLIENT_ID`, `DDNS_GOOGLE_CLIENT_SECRET` and `DDNS_ADMIN_EMAIL` (the only Google account allowed
   to sign in) in the `ddns` environment of `docker-compose.override.yml`, next to `DDNS_PUBLIC_URL`.
3. Run `docker compose --project-name ddns up -d` again.

The password login stays available as long as a password is set, so you can always get in from the server.

### Backups

The `backup` service takes a compressed backup of all `ddns` data (hosts, DNSSEC keys and the admin password) every
day into `docker/.backups` and deletes backups older than 180 days. It runs on every server, including secondaries.
The backup files belong to root; work with them through the `backup` service, which has the directory mounted at
`/backups`. To restore the newest backup, or a given one:

```
docker compose --project-name ddns exec backup ddns load --input /backups
docker compose --project-name ddns exec backup ddns load --input /backups/ddns-backup-<time>.json.gz.age
```

To take a backup right away, for example before an upgrade:

```
docker compose --project-name ddns exec backup ddns backup --once
```

#### Encryption

Backups contain the private DNSSEC key, so set a passphrase to encrypt them: fill in `x-backup-passphrase` at the top
of `docker-compose.override.yml`, which passes it to the `ddns` and `backup` services as `DDNS_BACKUP_PASSPHRASE`.
Generate one with `openssl rand -base64 32` and keep a copy outside the server, as backups cannot be restored without
it. Use the same passphrase on a secondary.

With a passphrase set, backups end in `.age` and dumps are encrypted as well; `ddns load` decrypts them with the
passphrase it finds. Instead of the environment, the passphrase can come from a file: `--passphrase-file <path>` on
the `dump`, `load` and `backup` commands, or `DDNS_BACKUP_PASSPHRASE_FILE`. Encrypted files are standard
[age](https://age-encryption.org) files, so they can also be read without `ddns`:

```
age -d ddns-backup-<time>.json.gz.age | gunzip
```

`ddns dump --plain` writes an unencrypted dump even with a passphrase set.

Loading replaces all `ddns` data in Redis in one step. `ddns dump` writes the current data to stdout, for a backup on
demand. The Redis instance has to be dedicated to `ddns`, as hosts are stored as top level keys.

### Secondary nameserver

A second server can answer for the zone as well, so DNS keeps working when one of them is down. The primary keeps
taking all updates and runs the admin panel; the secondary only answers DNS from a copy of the primary's data.

1. Give the secondary a name, e.g. `ns2.example.net`, with an `A`/`AAAA` record, and add it as a second `NS` record
   for `d.example.net` in the parent zone next to `ddns.example.net`.
2. On the primary, set `DDNS_NAMESERVERS: ddns.example.net,ns2.example.net` in `docker-compose.override.yml` and run
   `docker compose --project-name ddns up -d` again, so the zone announces both nameservers.
3. On the secondary, clone the repository and use
   [the secondary sample](./docker/docker-compose.override.yml.secondary.sample) as `docker-compose.override.yml`,
   with the same domain settings as the primary. `DDNS_REPLICA: "true"` makes it serve DNS only: no web frontend, no
   admin panel and no DNSSEC key of its own. Start it with `docker compose --project-name ddns up -d --build`.
4. On the primary, copy the data over SSH. This needs a key based SSH login to a user on the secondary that may run
   `docker compose`:

   ```
   docker/ddns-sync.sh deploy@ns2.example.net /opt/ddns/docker
   ```

   It dumps the data on the primary and loads it on the secondary, replacing what was there. Both servers then sign
   with the same DNSSEC key, so the DS record stays the same. With a [backup passphrase](#encryption) set, the data is
   encrypted on the primary and decrypted on the secondary, which needs the same passphrase in its
   `docker-compose.override.yml`.
5. Keep the secondary current by running the sync every minute from cron on the primary:

   ```
   * * * * * flock -n /tmp/ddns-sync.lock /opt/ddns/docker/ddns-sync.sh deploy@ns2.example.net /opt/ddns/docker
   ```

   Address changes then reach the secondary within a minute, and hosts expire on both servers at the same time.

Run the first sync before the secondary's `NS` record goes live, as the secondary has no DNSSEC key until then.

### Health checks

`https://ddns.example.net/healthz` answers `{"status":"ok"}` with status 200 while `ddns` can reach Redis, and 503
otherwise, for uptime monitors. Docker checks the same on every server through the internal backend port, so
`docker compose ps` shows `healthy`. A secondary also reports when its data was taken (`data_from`); inside the
container, `wget -qO- "http://127.0.0.1:8053/healthz?max_age=10m"` fails when the last sync is older than 10 minutes.
Monitor a secondary from outside with a DNS query, for example for one of your hosts.
