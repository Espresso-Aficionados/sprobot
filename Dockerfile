FROM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS base
WORKDIR /build
COPY go.mod go.sum ./
# Modules are downloaded into the layer, not a cache mount, so layer caching
# (including CI's) covers them and the test image runs without re-downloading.
RUN go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN mkdir /empty-dir

FROM base AS build-sprobot
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sprobot ./cmd/sprobot

FROM base AS build-sprobot-web
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sprobot-web ./cmd/sprobot-web

FROM base AS build-stickybot
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /stickybot ./cmd/stickybot

FROM base AS build-threadbot
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /threadbot ./cmd/threadbot

# The distroless base is pinned as :latest@sha256 (index digest). Dependabot
# tracks it: a refreshed :latest arrives as a digest-bump PR.
FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2 AS prod
COPY --from=build-sprobot /sprobot /sprobot
COPY --from=base --chown=nonroot:nonroot /empty-dir /sprobot-cache
VOLUME /sprobot-cache
USER nonroot:nonroot
CMD ["/sprobot"]

FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2 AS prodweb
ENV PORT=9001
COPY --from=build-sprobot-web /sprobot-web /sprobot-web
USER nonroot:nonroot
CMD ["/sprobot-web"]

FROM build-sprobot AS dev
VOLUME /sprobot-cache
CMD ["/sprobot"]

FROM build-sprobot-web AS devweb
ENV WEB_ENDPOINT=http://localhost:8080/
ENV PORT=8080
CMD ["/sprobot-web"]

FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2 AS prodstickybot
COPY --from=build-stickybot /stickybot /stickybot
USER nonroot:nonroot
CMD ["/stickybot"]

FROM build-stickybot AS devstickybot
CMD ["/stickybot"]

FROM gcr.io/distroless/static-debian12:latest@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2 AS prodthreadbot
COPY --from=build-threadbot /threadbot /threadbot
USER nonroot:nonroot
CMD ["/threadbot"]

FROM build-threadbot AS devthreadbot
CMD ["/threadbot"]

# The test image carries the module cache but no build cache: the packages
# to compile are only known from our sources, which change on every PR.
# Instead, CI mounts a Go build cache it keeps between runs at
# /root/.cache/go-build when it runs this image (see test.yaml).
FROM base AS test
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { echo 'gofmt check failed:'; gofmt -l .; exit 1; } && go vet ./... && go test -race ./..."]
