#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target devgobot -t sprobot-go-dev .
docker run --rm -it --env-file ./config/config.env sprobot-go-dev "$@"
