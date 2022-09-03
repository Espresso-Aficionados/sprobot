#!/bin/bash
set -euo pipefail
docker build --target devweb -t sprobot-dev .
docker run --rm -it --env-file ./config/config.env -p 80:8081 sprobot-dev $@
