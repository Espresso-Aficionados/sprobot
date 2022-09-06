#!/bin/bash
set -eou pipefail

echo "Running mypy on sprobot"
cd /code/sprobot && time mypy --strict --explicit-package-bases --namespace-packages .

echo "Running mypy on sprobot-web"
cd /code/sprobot-web && time mypy --strict --explicit-package-bases --namespace-packages .

cd /code/
isort --profile=black --check --diff .
flake8 -v --config=/code/testing/flake8.ini
