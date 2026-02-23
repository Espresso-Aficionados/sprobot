FROM golang:1.26-bookworm AS base
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/

FROM base AS build-sprobot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /sprobot ./cmd/sprobot

FROM base AS build-sprobot-web
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /sprobot-web ./cmd/sprobot-web

FROM base AS build-stickybot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /stickybot ./cmd/stickybot

FROM base AS build-threadbot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /threadbot ./cmd/threadbot

FROM gcr.io/distroless/static-debian12 AS prod
ENV SPROBOT_ENV=prod
COPY --from=build-sprobot /sprobot /sprobot
CMD ["/sprobot"]

FROM gcr.io/distroless/static-debian12 AS prodweb
ENV SPROBOT_ENV=prod
ENV PORT=9001
COPY --from=build-sprobot-web /sprobot-web /sprobot-web
CMD ["/sprobot-web"]

FROM build-sprobot AS dev
ENV SPROBOT_ENV=dev
CMD ["/sprobot"]

FROM build-sprobot-web AS devweb
ENV SPROBOT_ENV=dev
ENV PORT=8080
CMD ["/sprobot-web"]

FROM gcr.io/distroless/static-debian12 AS prodstickybot
ENV STICKYBOT_ENV=prod
COPY --from=build-stickybot /stickybot /stickybot
CMD ["/stickybot"]

FROM build-stickybot AS devstickybot
ENV STICKYBOT_ENV=dev
CMD ["/stickybot"]

FROM gcr.io/distroless/static-debian12 AS prodthreadbot
ENV THREADBOT_ENV=prod
COPY --from=build-threadbot /threadbot /threadbot
CMD ["/threadbot"]

FROM build-threadbot AS devthreadbot
ENV THREADBOT_ENV=dev
CMD ["/threadbot"]

FROM golang:1.26-bookworm AS test
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { echo 'gofmt check failed:'; gofmt -l .; exit 1; } && go vet ./... && go test ./..."]
