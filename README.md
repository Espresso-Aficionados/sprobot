# sprobot
[![publish](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/publish.yaml/badge.svg?branch=main)](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/publish.yaml)

This repo contains three Discord bots and a web server for the Espresso Aficionados Discord server. Each bot is a separate Discord application with its own token.

## Bots

### sprobot

Profile bot. Members create and view profiles using slash commands that are dynamically generated from templates.

- `/edit<template>` / `/get<template>` — edit your profile, or view a member's profile
- `/delete<template>` — delete your entire profile or just its picture
- `/wiki` — search the wiki (with autocomplete)
- `/s` — post a shortcut response (round-robin through configured responses, with autocomplete)
- `/warn user reason` — issue a warning to a member
- `/temprole user role` — assign a whitelisted temporary role to a member
- `/market progress user` — check a member's progress toward the marketplace poster role
- `/market leaderboard` — show the members closest to the marketplace poster role
- `/topposters` — show the top posters over the last 7 days
- `/config <group> <subcommand>` — configure the bot (groups below)
- "Get <Name> Profile" user and message context menus (one pair per template) — view a member's profile
- "Save message to mod log" message context menu

`/market` replies are hidden unless `public` is set. `/warn`, `/temprole`, `/market`, `/topposters`, `/config` and the mod log context menu require Manage Messages.

`/config` groups:

| Group | Subcommands | Configures |
|---|---|---|
| `shortcuts` | `set`, `remove`, `list` | `/s` shortcuts |
| `welcome` | `set`, `clear`, `show`, `test`, `enable`, `disable` | welcome DM for new members |
| `renamelog` | `set`, `add`, `remove`, `list`, `clear` | logging of channel and thread renames |
| `market` | `config`, `show-config`, `ban`, `unban`, `banlist`, `filter-add`, `filter-remove`, `filter-list`, `filter-clear` | marketplace poster role |
| `autorole` | `set`, `show`, `clear` | role given to new members |
| `eventlog` | `set`, `show`, `clear` | event log channel |
| `modlog` | `set`, `show`, `clear`, `audit` | mod log forum channel |
| `temprole` | `add`, `remove`, `list` | temporary role whitelist |
| `topposters` | `filter-add`, `filter-remove`, `filter-list`, `filter-clear` | users, roles and channels left out of `/topposters` |
| `starboard` | `config`, `show-config`, `disable`, `filter-add`, `filter-remove`, `filter-list`, `filter-clear` | starboard channel, emoji, threshold and filtered channels |
| `migrate-links` | `dry-run`, `run` | temporary: points old bot messages' file links at stable links |

**Features:** ticket system, self-assign role panels, temporary role assignment with auto-expiry, mod log, event logging (join/leave/ban/kick/timeout/message edit and delete/channel and role changes/emoji, sticker, stage instance, scheduled event, and soundboard sound changes), channel and thread rename logging, welcome DMs, auto-role on join, poster role tracking, starboard, forum helper reminders, message cache for edit/delete logging.

### stickybot

Reposts a "sticky" message at the bottom of a channel as new messages arrive. Rate-limited by both a message count threshold and a time delay.

- "Sticky this message" context menu — stick a message to a channel
- `/sticky stop` / `start` / `remove` / `list` — manage stickies

All commands require Manage Messages permission.

### threadbot

Posts a reminder listing a channel's active threads and reposts it at the bottom of the channel as the conversation moves on. Configurable idle timers and message/time thresholds. When the reminder is still the last message in the channel, it edits the existing message in place instead of deleting and reposting, reducing message churn.

- `/threadbot enable` — enable reminders in the current channel (optional `min_idle`, `max_idle`, `msg_threshold`, `time_threshold`, `buffer`)
- `/threadbot disable` — disable reminders in the current channel
- `/threadbot list` — list channels with reminders in this server
- `/threadbot refreshcounts` — clear cached thread member counts so they are fetched again
- `/threadbot fetchall` — show all active threads across the server (hidden unless `public` is set)
- `/threadbot hide <thread>` / `/threadbot unhide <thread>` — never promote a thread in reminders or `/threads` (until unhidden or deleted)
- `/threadbot hidden` — list hidden threads
- `/threads` — show active threads in the current channel

`/threadbot` requires Manage Messages and works in channels, not threads. `/threads` is open to everyone.

### sprobot-web

Web server that renders profile pages and serves files saved by the bots (mod log, shortcut and sticky attachments) from S3. It serves the profile and file URLs linked from Discord messages and makes no Discord API calls.

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
- `docs/` — project documentation, including Discord privileged-intent evidence
- `PRIVACY.md` / `TERMS.md` — privacy policy and terms of service for the bots and web server
- `.github/` — CI workflows and Dependabot configuration

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
| `PORT` | Web server port (default 8080; the prod `sadbox/sprobot-web` image sets `PORT=9001`) |

## Deployment

Pull requests run the gofmt check, `go vet` and the tests on amd64 and arm64, and build the prod images for amd64 without pushing them.

Multiarch Docker images are built and pushed to Docker Hub on every push to `main` and on a Mon/Wed/Fri 05:00 UTC schedule, which picks up base image updates:

- `sadbox/sprobot`
- `sadbox/sprobot-web`
- `sadbox/stickybot`
- `sadbox/threadbot`

A publish moves `latest` for all four images only if its commit is still the tip of `main`. The first images built from a commit are tagged `sha-<short commit SHA>`, and that tag never moves; later builds of the same commit (scheduled rebuilds with refreshed base images, re-runs) are tagged `sha-<short commit SHA>-<UTC build time>` instead. Images carry the full commit SHA in the `org.opencontainers.image.revision` label, so a running image can be traced to its commit and rolled back by tag.

## Style

Code must pass `gofmt`. This is checked automatically in CI.

The repo has automatic pull requests submitted by dependabot when dependencies are updated.
