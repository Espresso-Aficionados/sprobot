#!/bin/bash
set -euo pipefail
docker run --rm -it --env-file ./config/config.env --pull always sadbox/sprobot:latest $@
