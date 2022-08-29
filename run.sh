#!/bin/bash
set -euo pipefail
docker build -t sprobot .
docker run -it --mount="type=bind,source=$(grealpath config),target=/config" sprobot $@
