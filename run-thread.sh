#!/bin/bash
set -euo pipefail
DOCKER_BUILDKIT=1 docker build --target devthreadbot -t threadbot-dev .
exec docker run --rm -it --init --env-file ./config/config.env threadbot-dev "$@"
