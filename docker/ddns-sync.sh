#!/usr/bin/env bash
# Copies all ddns data (hosts, DNSSEC keys, admin credentials) from this
# primary server to a secondary server over SSH and loads it there, replacing
# the data of the secondary in one step.
#
# Usage, on the primary:
#   docker/ddns-sync.sh [user@]secondary [ddns docker directory on the secondary]
#
# The SSH user on the secondary needs to be allowed to run docker compose.
# Both ends run in the backup service, which holds the backup settings.
set -euo pipefail

if [ $# -lt 1 ]; then
    echo "usage: $0 [user@]secondary [ddns docker directory on the secondary, default ddns/docker]" >&2
    exit 2
fi

target=$1
remote_dir=${2:-ddns/docker}

cd "$(dirname "$0")"

docker compose --project-name ddns exec -T backup ddns dump --gzip \
    | ssh -o BatchMode=yes "$target" \
        "cd '$remote_dir' && docker compose --project-name ddns exec -T backup ddns load"
