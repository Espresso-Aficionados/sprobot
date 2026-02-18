#!/bin/bash
set -euo pipefail
docker buildx build --load --target test -t sprobot-test .
docker run --rm sprobot-test
