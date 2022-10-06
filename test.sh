#!/bin/bash
set -euo pipefail

docker build --target lint -t sprobot-lint .
docker run --rm -it --env-file ./config/config.env -v sprobot-mypy-cache:/code/sprobot/.mypy_cache -v sprobot-web-mypy-cache:/code/sprobot-web/.mypy_cache sprobot-lint

docker build --target test -t sprobot-test .
docker run --rm -it --env-file ./config/config.env sprobot-test 
