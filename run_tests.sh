#!/bin/bash
set -euo pipefail

# This silly process emulates what the automated tests do for docker stuff
# The entry point is src/test/main.py

docker-compose -f ./docker-compose.test.yml -p ci build
docker-compose -f ./docker-compose.test.yml -p ci up -d
docker logs -f ci_sut_1
docker wait ci_sut_1
docker rm ci_sut_1
