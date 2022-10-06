#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target dev -t sprobot-dev .
docker run --rm -it --env-file ./config/config.env sprobot-dev $@
