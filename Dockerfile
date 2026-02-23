ARG TARGET_DIST="gcr.io/distroless/static-debian12"

FROM golang:1.26 AS base
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN mkdir /empty-dir

FROM base AS build-sprobot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sprobot ./cmd/sprobot

FROM base AS build-sprobot-web
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sprobot-web ./cmd/sprobot-web

FROM base AS build-stickybot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /stickybot ./cmd/stickybot

FROM base AS build-threadbot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /threadbot ./cmd/threadbot

FROM ${TARGET_DIST} AS prod
ENV SPROBOT_ENV=prod
COPY --from=build-sprobot /sprobot /sprobot
COPY --from=base --chown=nonroot:nonroot /empty-dir /sprobot-cache
VOLUME /sprobot-cache
USER nonroot:nonroot
CMD ["/sprobot"]

FROM ${TARGET_DIST} AS prodweb
ENV SPROBOT_ENV=prod
ENV PORT=9001
COPY --from=build-sprobot-web /sprobot-web /sprobot-web
USER nonroot:nonroot
CMD ["/sprobot-web"]

FROM build-sprobot AS dev
ENV SPROBOT_ENV=dev
VOLUME /sprobot-cache
CMD ["/sprobot"]

FROM build-sprobot-web AS devweb
ENV SPROBOT_ENV=dev
ENV PORT=8080
CMD ["/sprobot-web"]

FROM ${TARGET_DIST} AS prodstickybot
ENV STICKYBOT_ENV=prod
COPY --from=build-stickybot /stickybot /stickybot
USER nonroot:nonroot
CMD ["/stickybot"]

FROM build-stickybot AS devstickybot
ENV STICKYBOT_ENV=dev
CMD ["/stickybot"]

FROM ${TARGET_DIST} AS prodthreadbot
ENV THREADBOT_ENV=prod
COPY --from=build-threadbot /threadbot /threadbot
USER nonroot:nonroot
CMD ["/threadbot"]

FROM build-threadbot AS devthreadbot
ENV THREADBOT_ENV=dev
CMD ["/threadbot"]

FROM base AS test
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { echo 'gofmt check failed:'; gofmt -l .; exit 1; } && go vet ./... && go test ./..."]
