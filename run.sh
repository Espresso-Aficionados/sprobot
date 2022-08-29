#!/bin/bash
set -euo pipefail
image_sha=$(docker build -q .)
docker run --rm -it --mount="type=bind,source=$(grealpath config),target=/config" "$image_sha" $@
