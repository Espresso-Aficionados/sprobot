#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target devstickybot -t stickybot-dev .
exec docker run --rm -it --init --env-file ./config/config.env stickybot-dev "$@"
