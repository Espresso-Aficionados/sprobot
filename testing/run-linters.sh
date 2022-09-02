#!/bin/bash
set -eou pipefail

pytype --config ./testing/pytype.cfg ./sprobot/
flake8 -v --config=/code/testing/flake8.ini
