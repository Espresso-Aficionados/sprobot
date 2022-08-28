#!/bin/bash
./build.sh
docker run -it --mount="type=bind,source=$(grealpath config),target=/config" sprobot $@
