#!/bin/bash
set -eou pipefail

pytype .
flake8 -v --config=/code/testing/flake8.ini
