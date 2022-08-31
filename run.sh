#!/bin/bash
set -euo pipefail
image_sha=$(docker build -q .)
docker run --rm -it --env-file ./config/config.env "$image_sha" $@
