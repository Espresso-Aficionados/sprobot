#!/bin/bash
docker buildx build --no-cache --push --target prod --platform linux/arm64,linux/amd64 -t sadbox/sprobot .
docker buildx stop
