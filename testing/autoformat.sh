#!/bin/bash
set -euo pipefail

echo "Formatting code"
black /local_code
isort --py 310 --profile black /local_code
