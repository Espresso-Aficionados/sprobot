#!/bin/bash
set -eou pipefail

flake8 -v --config=/code/testing/flake8.ini
