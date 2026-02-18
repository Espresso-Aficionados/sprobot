#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target dev -t sprobot-dev .
exec docker run --rm -it --init --env-file ./config/config.env sprobot-dev "$@"
