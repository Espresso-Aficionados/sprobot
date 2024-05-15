#!/bin/bash
set -exou pipefail

echo $PWD
ls -la

echo "Running pyright"
time pyright --warnings -p /testing/pyrightconfig.json .

cd /code/
echo "RUNNING isort"
isort --profile=black --check --diff .

echo "RUNNING flake8"
flake8 -v --config=/testing/flake8.ini
