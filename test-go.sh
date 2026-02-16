#!/bin/bash
set -euo pipefail
docker buildx build --load --target gotest -t sprobot-gotest .
docker run --rm sprobot-gotest
