# sprobot
[![sprobot build, test, and maybe push](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/build-test-push.yaml/badge.svg?branch=main)](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/build-test-push.yaml)

This repo contains three Discord bots and a web server for the Espresso Aficionados Discord server.

## Bots

### sprobot

Profile bot. Members create and view profiles using slash commands that are dynamically generated from templates.

- `/edit<template>` / `/get<template>` / `/delete<template>` — edit, view, or delete a profile
- `/wiki` — search the wiki (with autocomplete)
- `/s` — post a shortcut response (round-robin through configured responses, with autocomplete)
- `/sconfig set|remove|list` — manage shortcuts (requires Manage Messages)
- `/welcome set|clear|show|test|enable|disable` — manage welcome DMs for new members (requires Manage Messages)
- `/warn` — issue a warning to a member (requires Manage Messages)
- `/marketprogress` — check a member's poster role progress (requires Manage Messages)
- `/topposters` — show top posters for the guild (requires Manage Messages)
- Context menus for saving messages to the mod log and viewing profiles

**Features:** ticket system, self-assign role panels, event logging (join/leave/ban/kick/timeout/message edit and delete/channel and role changes), auto-role on join, poster role tracking, forum helper reminders, message cache for edit/delete logging.

### stickybot

Reposts a "sticky" message at the bottom of a channel as new messages arrive. Rate-limited by both a message count threshold and a time delay.

- "Sticky this message" context menu — stick a message to a channel
- `/sticky stop` / `start` / `remove` / `list` — manage stickies

All commands require Manage Messages permission.

### threadbot

Posts periodic reminders in threads to keep them active. Configurable idle timers and message thresholds. When the reminder is still the last message in the channel, it edits the existing message in place instead of deleting and reposting, reducing message churn.

- `/threadbot enable` — enable reminders in the current thread (with optional min_idle, max_idle, msg_threshold, time_threshold)
- `/threadbot disable` — disable reminders
- `/threadbot list` — list active reminders
- `/threads` — show active threads in the current channel

### sprobot-web

Web server that renders profile pages from S3 data. Serves profile URLs linked from Discord embeds.

## Project Structure

- `cmd/sprobot/` — sprobot entrypoint
- `cmd/sprobot-web/` — web server entrypoint
- `cmd/stickybot/` — stickybot entrypoint
- `cmd/threadbot/` — threadbot entrypoint
- `pkg/bot/` — sprobot logic (commands, modals, event handlers)
- `pkg/stickybot/` — stickybot logic
- `pkg/threadbot/` — threadbot logic
- `pkg/botutil/` — shared bot utilities (base struct, guild config, retry helpers)
- `pkg/idleloop/` — shared idle-repost loop used by stickybot and threadbot
- `pkg/testutil/` — shared test helpers (fake S3, test client)
- `pkg/sprobot/` — shared types (templates, links, config)
- `pkg/s3client/` — S3 storage client

## Quickstart

Copy `config/example.env` to `config/config.env` and fill in the values.

Run tests:

```
./test.sh
```

Run individual bots in dev mode:

```
./run.sh          # sprobot
./run-web.sh      # web server
./run-sticky.sh   # stickybot
./run-thread.sh   # threadbot
```

Or run everything together:

```
./run-all.sh
```

### Testing without Docker

```
go build ./...
go vet ./...
go test ./...
```

## Environment Variables

See `config/example.env` for the full list. Key variables:

| Variable | Description |
|---|---|
| `SPROBOT_DISCORD_TOKEN` | Discord token for sprobot |
| `STICKYBOT_DISCORD_TOKEN` | Discord token for stickybot |
| `THREADBOT_DISCORD_TOKEN` | Discord token for threadbot |
| `S3_KEY` / `S3_SECRET` / `S3_ENDPOINT` / `S3_BUCKET` | S3-compatible storage credentials (shared by all bots) |
| `WEB_ENDPOINT` | Profile web URL base (defaults to `https://bot.espressoaf.com/`; set to `http://localhost:8080/` for dev) |
| `SPROBOT_HEALTHCHECK_ENDPOINT` | Healthcheck ping URL for sprobot (optional) |
| `STICKYBOT_HEALTHCHECK_ENDPOINT` | Healthcheck ping URL for stickybot (optional) |
| `THREADBOT_HEALTHCHECK_ENDPOINT` | Healthcheck ping URL for threadbot (optional) |
| `PORT` | Web server port (default 8080) |

## Deployment

Multiarch Docker images are automatically built and pushed to Docker Hub on commits to main:

- `sadbox/sprobot`
- `sadbox/sprobot-web`
- `sadbox/stickybot`
- `sadbox/threadbot`

## Style

Code must pass `gofmt`. This is checked automatically in CI.

The repo has automatic pull requests submitted by dependabot when dependencies are updated.
