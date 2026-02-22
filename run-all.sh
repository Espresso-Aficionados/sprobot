#!/bin/bash
set -euo pipefail
exec docker compose -f docker-compose.dev.yml up --build "$@"
