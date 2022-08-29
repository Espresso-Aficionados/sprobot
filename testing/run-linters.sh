#!/bin/bash
set -eou pipefail

mypy .
flake8 -v --config=/code/testing/flake8.ini
