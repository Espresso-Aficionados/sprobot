FROM golang:1.27.3 AS base
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

# The distroless base is untagged so the Mon/Wed/Fri publish rebuilds pick up
# its updates. Dependabot skips FROM lines with no tag or digest, so it does
# not track this base; pinning a digest would let it, but would stop those
# rebuilds from refreshing the base until its PRs merge.
FROM gcr.io/distroless/static-debian12 AS prod
COPY --from=build-sprobot /sprobot /sprobot
COPY --from=base --chown=nonroot:nonroot /empty-dir /sprobot-cache
VOLUME /sprobot-cache
USER nonroot:nonroot
CMD ["/sprobot"]

FROM gcr.io/distroless/static-debian12 AS prodweb
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

FROM gcr.io/distroless/static-debian12 AS prodstickybot
COPY --from=build-stickybot /stickybot /stickybot
USER nonroot:nonroot
CMD ["/stickybot"]

FROM build-stickybot AS devstickybot
CMD ["/stickybot"]

FROM gcr.io/distroless/static-debian12 AS prodthreadbot
COPY --from=build-threadbot /threadbot /threadbot
USER nonroot:nonroot
CMD ["/threadbot"]

FROM build-threadbot AS devthreadbot
CMD ["/threadbot"]

# The test image carries the module cache but no build cache. The dependency
# packages to precompile are only known from our sources, which change on
# every PR, so warming the build cache here would just move the cold compile
# from docker run to docker build.
FROM base AS test
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { echo 'gofmt check failed:'; gofmt -l .; exit 1; } && go vet ./... && go test -race ./..."]
