| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_RESOURCE_ATTRIBUTES`, other `OTEL_*` | OpenTelemetry metrics and traces over OTLP/HTTP (optional; the bots and sprobot-web, not `s3-orphans` or `-check-state`). Read by the OpenTelemetry SDK, not checked at startup; unset means no export. A bad value turns telemetry off with a warning, never the program. |
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

The starboard never reposts messages from private threads, reposts messages from age-restricted channels only to an age-restricted starboard channel, and skips channels and categories on its filter list, threads included. It doesn't compare who can see the source channel with who can see the starboard, so add any channel whose messages shouldn't be reposted to the filter list.

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
- `pkg/telemetrytest/` — in-memory OpenTelemetry recorder for tests
- `pkg/sprobot/` — shared types (templates, links, config)
- `pkg/s3client/` — S3 storage client
- `docs/` — project documentation, including Discord privileged-intent evidence
- `PRIVACY.md` / `TERMS.md` — privacy policy and terms of service for the bots and web server
- `.github/` — CI workflows and Dependabot configuration

## Quickstart

Copy `config/example.env` to `config/config.env` and fill in the values. Secrets may be 1Password references (`op://vault/item/field`): the run scripts resolve them with `op run` using your own 1Password account, so they need the [1Password CLI](https://developer.1password.com/docs/cli/) with desktop-app integration turned on.

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
go test -race ./...
```

## Environment Variables

See `config/example.env` for the full list. Each program checks its whole environment at startup, before it connects to anything: a missing or malformed variable logs one `Invalid environment` line per problem, naming the variable (never its value), and exits 1. Every value must be plain: the programs don't talk to 1Password. Secrets are rendered before the container starts, in prod on the host (homelab-configs `quadlet/render-env.sh`) and in dev by the run scripts' `op run`. A value that is still a 1Password reference (`op://…`) is refused as a problem naming the variable. Key variables:

| Variable | Description |
|---|---|
| `SPROBOT_DISCORD_TOKEN` | Discord token for sprobot |
| `STICKYBOT_DISCORD_TOKEN` | Discord token for stickybot |
| `THREADBOT_DISCORD_TOKEN` | Discord token for threadbot |
| `S3_KEY` / `S3_SECRET` / `S3_ENDPOINT` / `S3_BUCKET` | S3-compatible storage credentials (shared by all bots) |
| `WEB_ENDPOINT` | Profile web URL base, ending in `/` (defaults to `https://bot.espressoaf.com/`; set to `http://localhost:8080/` for dev) |
| `EGRESS_FETCH_GATEWAY` | The bots only, optional: the egress proxy's fetch gateway (plain `http://host:port`). When set, images from user-supplied URLs (profile and shortcut images) are fetched by the gateway, not by the bot. Unset: the bot fetches them itself |
| `PORT` | Web server port (default 8080; the prod `sprobot-web` image sets `PORT=9001`) |

## Deployment

Pull requests run the gofmt check, `go vet` and the tests on amd64 and arm64, and build the prod images for amd64 without pushing them.

Multiarch images are built and pushed to the GitHub Container Registry on every push to `main` that changes more than docs (`*.md`, `docs`, `LICENSE`), and when run by hand. Base image updates arrive as Dependabot PRs (the images are pinned by digest):

- `ghcr.io/espresso-aficionados/sprobot`
- `ghcr.io/espresso-aficionados/sprobot-web`
- `ghcr.io/espresso-aficionados/stickybot`
- `ghcr.io/espresso-aficionados/threadbot`

They were published to Docker Hub as `sadbox/*` until November 2026; those images are no longer updated.

A publish moves `latest` for all four images only if its commit is still the tip of `main`. The first images built from a commit are tagged `sha-<short commit SHA>`, and that tag never moves; later builds of the same commit (re-runs, manual runs) are tagged `sha-<short commit SHA>-<UTC build time>` instead. Images carry the full commit SHA in the `org.opencontainers.image.revision` label, so a running image can be traced to its commit and rolled back by tag.

Every state document the bots persist (the per-guild JSON in S3, and sprobot's `/sprobot-cache`) has a state version; one without a `"version"` is version 1. A build refuses state a newer build wrote: it logs which document and exits 1 at startup, and never rewrites, backs up or deletes it. Before rolling an image back, or as a start gate, run its `-check-state` (add `-json` for one JSON object). It reads that bot's state read-only: S3 GETs for every allowed guild, plus sprobot's cache dir. It prints each document's version and exits 0 if the build can run on it, 3 if a newer build wrote some of it, or 1 if some can't be read. It needs the S3 variables (`S3_*`) and network, but not the Discord token. sprobot-web keeps no state, so its `-check-state` always exits 0. Images from before state versions ignore their arguments: `-check-state` on one starts the bot.

Each image carries BuildKit provenance and an SBOM, plus a GitHub build-provenance attestation: `gh attestation verify oci://ghcr.io/espresso-aficionados/sprobot:latest -R Espresso-Aficionados/sprobot`.

## Style

Code must pass `gofmt`. This is checked automatically in CI.

The repo has automatic pull requests submitted by dependabot when dependencies are updated.
