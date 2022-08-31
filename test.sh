#!/bin/bash
set -euo pipefail

docker build --target lint -t sprobot-lint .
docker run --rm -it --env-file ./config/config.env sprobot-lint

docker build --target test -t sprobot-test .
docker run --rm -it --env-file ./config/config.env sprobot-test 
