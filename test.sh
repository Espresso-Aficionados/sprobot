#!/bin/bash
set -euo pipefail

docker buildx build --load --target lint --platform linux/arm64,linux/amd64 -t sprobot-lint .
docker buildx build --load --target test --platform linux/arm64,linux/amd64 -t sprobot-test .

for platform in linux/arm64 linux/amd64; do
    docker run --rm -it  --platform $platform --env-file ./config/config.env -v sprobot-mypy-cache:/code/sprobot/.mypy_cache -v sprobot-web-mypy-cache:/code/sprobot-web/.mypy_cache sprobot-lint
    docker run --rm -it  --platform $platform --env-file ./config/config.env sprobot-test 
done
