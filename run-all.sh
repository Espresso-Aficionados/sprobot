#!/bin/bash
set -euo pipefail
. ./dev-env.sh
override=$(mktemp "${TMPDIR:-/tmp}/sprobot-dev-env.XXXXXX")
trap 'rm -f "$override"' EXIT
dev_compose_override "$override"
op run --no-masking --env-file="$DEV_ENV_FILE" -- \
  docker compose -f docker-compose.dev.yml -f "$override" up --build "$@"
