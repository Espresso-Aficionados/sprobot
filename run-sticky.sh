#!/bin/bash
set -euo pipefail
. ./dev-env.sh
DOCKER_BUILDKIT=1 docker build --target devstickybot -t stickybot-dev .
# shellcheck disable=SC2086 # DOCKER_ENV_FLAGS is one "-e NAME" pair per variable
exec op run --no-masking --env-file="$DEV_ENV_FILE" -- docker run --rm -it --init $DOCKER_ENV_FLAGS stickybot-dev "$@"
