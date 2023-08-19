#!/bin/bash
set -euo pipefail

base_path="$(grealpath $(dirname $0))"

echo "Formatting code in $base_path"
black "$base_path"
isort --py 310 --profile black "$base_path"
