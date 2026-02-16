# set base image (host OS)
FROM python:3.10.19 AS base

# set the working directory in the container
WORKDIR /code

RUN rm -f /etc/apt/apt.conf.d/docker-clean; echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache
ARG DEBIAN_FRONTEND=noninteractive
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked apt update
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked apt install -y nodejs npm

# copy the dependencies file to the working directory
COPY requirements.txt .

# install dependencies
RUN --mount=type=cache,target=/root/.cache pip install -r requirements.txt

FROM base AS prod
ENV SPROBOT_ENV=prod
# copy the content of the local src directory to the working directory
COPY src/ .
CMD [ "python", "./sprobot/main.py" ]

FROM base AS prodweb
ENV SPROBOT_ENV=prod
COPY src/ .
WORKDIR /code/sprobot-web
ENV FLASK_APP=main
CMD [ "flask", "run", "--host", "0.0.0.0", "--port", "80" ]

# Dev stuff below here
FROM base AS devbase
ENV SPROBOT_ENV=dev
COPY requirements-dev.txt .
RUN --mount=type=cache,target=/root/.cache pip install -r requirements-dev.txt
RUN --mount=type=cache,target=/root/.npm npm install -g pyright
# copy our test runner
COPY src/ .
COPY testing/ /testing

FROM devbase AS dev
CMD [ "python", "./sprobot/main.py" ]

FROM devbase AS autoformat
CMD ["/testing/autoformat.sh"]

FROM devbase AS devweb
WORKDIR /code/sprobot-web
ENV FLASK_APP=main
CMD [ "flask", "run", "--host", "127.0.0.1", "--port", "8080", "--debug" ]

FROM devbase AS test
CMD ["/testing/run-tests.sh"]

FROM devbase AS lint
CMD ["/testing/run-linters.sh"]

# Go targets
FROM golang:1.26-bookworm AS gobuild
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /sprobot ./cmd/sprobot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /sprobot-web ./cmd/sprobot-web

FROM gcr.io/distroless/static-debian12 AS prodgobot
ENV SPROBOT_ENV=prod
COPY --from=gobuild /sprobot /sprobot
CMD ["/sprobot"]

FROM gcr.io/distroless/static-debian12 AS prodgoweb
ENV SPROBOT_ENV=prod
ENV PORT=9001
COPY --from=gobuild /sprobot-web /sprobot-web
CMD ["/sprobot-web"]

FROM gobuild AS devgobot
ENV SPROBOT_ENV=dev
CMD ["/sprobot"]

FROM gobuild AS devgoweb
ENV SPROBOT_ENV=dev
ENV PORT=8080
CMD ["/sprobot-web"]

FROM golang:1.26-bookworm AS gotest
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY pkg/ pkg/
CMD ["sh", "-c", "test -z \"$(gofmt -l .)\" || { echo 'gofmt check failed:'; gofmt -l .; exit 1; } && go vet ./... && go test ./..."]
