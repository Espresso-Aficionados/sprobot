#!/bin/bash
set -euo pipefail

for platform in linux/amd64 linux/arm64; do
    DOCKER_BUILDKIT=1 docker build --target lint --platform $platform -t sprobot-lint .
    docker run --rm -it --platform $platform --env-file ./config/config.env -v sprobot-mypy-cache:/code/sprobot/.mypy_cache -v sprobot-web-mypy-cache:/code/sprobot-web/.mypy_cache sprobot-lint

    DOCKER_BUILDKIT=1 docker build --target test --platform $platform -t sprobot-test .
    docker run --rm -it --platform $platform --env-file ./config/config.env sprobot-test 
done
