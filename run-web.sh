#!/bin/bash
set -euo pipefail
. ./dev-env.sh
DOCKER_BUILDKIT=1 docker build --target devweb -t sprobot-dev-web .
# shellcheck disable=SC2086 # DOCKER_ENV_FLAGS is one "-e NAME" pair per variable
op run --no-masking --env-file="$DEV_ENV_FILE" -- docker run --network host --rm -it $DOCKER_ENV_FLAGS sprobot-dev-web "$@"
