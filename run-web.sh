#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target devweb -t sprobot-dev .
docker run --network host --rm -it --env-file ./config/config.env sprobot-dev $@
