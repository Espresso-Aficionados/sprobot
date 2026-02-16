#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target devgobot -t sprobot-go-dev .
exec docker run --rm -it --init --env-file ./config/config.env sprobot-go-dev "$@"
