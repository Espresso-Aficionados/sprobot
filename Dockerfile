FROM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS base
WORKDIR /build
COPY go.mod go.sum ./
# Modules are downloaded into the layer, not a cache mount, so layer caching
# (including CI's) covers them and the test image runs without re-downloading.
RUN go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN mkdir /empty-dir

# VERSION is the build's short commit SHA (publish.yaml's build-args),
# linked into every binary as pkg/buildinfo.Version: what -version prints
# and telemetry sends as service.version. Each build stage then checks its
# binary says so: a -X that names nothing is no error.
FROM base AS build-sprobot
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/Espresso-Aficionados/sprobot/pkg/buildinfo.Version=${VERSION}" -o /sprobot ./cmd/sprobot
RUN test "$(/sprobot -version)" = "${VERSION}"

FROM base AS build-sprobot-web
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/Espresso-Aficionados/sprobot/pkg/buildinfo.Version=${VERSION}" -o /sprobot-web ./cmd/sprobot-web
RUN test "$(/sprobot-web -version)" = "${VERSION}"

FROM base AS build-stickybot
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/Espresso-Aficionados/sprobot/pkg/buildinfo.Version=${VERSION}" -o /stickybot ./cmd/stickybot
RUN test "$(/stickybot -version)" = "${VERSION}"

FROM base AS build-threadbot
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/Espresso-Aficionados/sprobot/pkg/buildinfo.Version=${VERSION}" -o /threadbot ./cmd/threadbot
RUN test "$(/threadbot -version)" = "${VERSION}"

# The distroless base is pinned as :nonroot@sha256 (index digest). Dependabot
# tracks it: a refreshed :nonroot arrives as a digest-bump PR. :nonroot
# already runs as nonroot (uid/gid 65532); USER below says so explicitly, by
# number (hadolint DL3066), and /sprobot-cache is owned by that uid, as on
# bonnet's volume.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS prod
COPY --from=build-sprobot /sprobot /sprobot
COPY --from=base --chown=65532:65532 /empty-dir /sprobot-cache
VOLUME /sprobot-cache
USER 65532:65532
CMD ["/sprobot"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS prodweb
ENV PORT=9001
COPY --from=build-sprobot-web /sprobot-web /sprobot-web
USER 65532:65532
CMD ["/sprobot-web"]

FROM build-sprobot AS dev
VOLUME /sprobot-cache
CMD ["/sprobot"]

FROM build-sprobot-web AS devweb
ENV WEB_ENDPOINT=http://localhost:8080/
ENV PORT=8080
CMD ["/sprobot-web"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS prodstickybot
COPY --from=build-stickybot /stickybot /stickybot
USER 65532:65532
CMD ["/stickybot"]

FROM build-stickybot AS devstickybot
CMD ["/stickybot"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS prodthreadbot
COPY --from=build-threadbot /threadbot /threadbot
USER 65532:65532
CMD ["/threadbot"]

FROM build-threadbot AS devthreadbot
CMD ["/threadbot"]

# The test image carries the module cache but no build cache: the packages
# to compile are only known from our sources, which change on every PR.
# Instead, CI mounts a Go build cache it keeps between runs at
# /root/.cache/go-build when it runs this image (see test.yaml).
FROM base AS test
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { echo 'gofmt check failed:'; gofmt -l .; exit 1; } && go vet ./... && go test -race ./..."]
