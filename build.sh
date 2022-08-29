#!/bin/bash
docker buildx build -t sprobot .
docker buildx stop
