# sprobot
[![sprobot build, test, and maybe push](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/build-test-push.yaml/badge.svg?branch=main)](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/build-test-push.yaml)

Espresso Discord Profile Bot

## Quickstart

Run tests locally using `./test.sh`, which runs gofmt, go vet, and all tests inside Docker.

Run the bot locally using `./run.sh`. This builds and runs a dev container.

Run the web server locally using `./run-web.sh`.

Multiarch deployments are automatically built and pushed to Docker Hub once a commit makes it to main.

## Project Structure

- `cmd/sprobot/` - Discord bot entrypoint
- `cmd/sprobot-web/` - Web server entrypoint
- `pkg/bot/` - Discord bot logic (commands, modals, event handlers)
- `pkg/sprobot/` - Shared types (templates, links, config)
- `pkg/s3client/` - S3 storage client

## For Contributors

### Testing

```
./test.sh
```

Or run locally without Docker:

```
go build ./...
go vet ./...
go test ./...
```

### Style

Code must pass `gofmt`. This is checked automatically in CI.

### Updates

The repo has automatic pull requests submitted by dependabot when dependencies are updated.
