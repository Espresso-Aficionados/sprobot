#!/bin/bash
set -euo pipefail

base_path="$(grealpath $(dirname $0))"

DOCKER_BUILDKIT=1 docker build --target autoformat -t sprobot-autoformat .
docker run --rm -it -v $(pwd)/src:/local_code sprobot-autoformat
