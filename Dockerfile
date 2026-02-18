FROM golang:1.26-bookworm AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /sprobot ./cmd/sprobot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /sprobot-web ./cmd/sprobot-web

FROM gcr.io/distroless/static-debian12 AS prod
ENV SPROBOT_ENV=prod
COPY --from=build /sprobot /sprobot
CMD ["/sprobot"]

FROM gcr.io/distroless/static-debian12 AS prodweb
ENV SPROBOT_ENV=prod
ENV PORT=9001
COPY --from=build /sprobot-web /sprobot-web
CMD ["/sprobot-web"]

FROM build AS dev
ENV SPROBOT_ENV=dev
CMD ["/sprobot"]

FROM build AS devweb
ENV SPROBOT_ENV=dev
ENV PORT=8080
CMD ["/sprobot-web"]

FROM golang:1.26-bookworm AS test
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { echo 'gofmt check failed:'; gofmt -l .; exit 1; } && go vet ./... && go test ./..."]
