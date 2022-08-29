#!/bin/bash
set -eou pipefail

mypy --strict . 
flake8 -v --config=/code/testing/flake8.ini
