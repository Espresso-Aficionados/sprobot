#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target devgoweb -t sprobot-go-web-dev .
docker run --network host --rm -it --env-file ./config/config.env sprobot-go-web-dev "$@"
