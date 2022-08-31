#!/bin/bash
set -euo pipefail
docker build --target dev -t sprobot-dev .
docker run --rm -it --env-file ./config/config.env sprobot-dev $@
