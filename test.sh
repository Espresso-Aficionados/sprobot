#!/bin/bash
set -euo pipefail

docker buildx build --load --target lint --platform linux/arm64,linux/amd64 -t sprobot-lint .
docker buildx build --load --target test --platform linux/arm64,linux/amd64 -t sprobot-test .

for platform in linux/arm64 linux/amd64; do
    docker run --rm -it  --platform $platform sprobot-lint
    docker run --rm -it  --platform $platform sprobot-test
done
