#!/bin/bash
docker buildx build --push --target prod --platform linux/arm64,linux/amd64 -t sadbox/sprobot .
docker buildx stop
