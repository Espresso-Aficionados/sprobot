# CLAUDE.md

Guidance for agents and new contributors working in this repo. The
[README](README.md) covers user-facing commands and environment variables. This
file covers how the code is put together and the rules that keep it shippable.

## What this is

Three Discord bots plus a web server for the Espresso Aficionados Discord
server, written in Go on [disgo](https://github.com/disgoorg/disgo). Each bot is
a **separate Discord application** with its own token.

| Binary | Package | Role |
|---|---|---|
| `cmd/sprobot` | `pkg/bot` | Main bot: profiles, tickets, event/mod logs, starboard, roles, shortcuts, wiki |
| `cmd/stickybot` | `pkg/stickybot` | Reposts sticky messages at the bottom of a channel |
| `cmd/threadbot` | `pkg/threadbot` | Active-thread reminders and `/threads` |
| `cmd/sprobot-web` | (`cmd/sprobot-web`) | Renders profile pages and stored files from S3. Makes no Discord API calls. |

Shared packages:
- `pkg/botutil`: `BaseBot`, healthcheck pings, `RunSaveLoop`, `PostWithRetry`,
  `RegisterGuildCommands`, `PrivacyPolicyStatus`.
- `pkg/idleloop`: the idle-repost loop used by stickybot and threadbot.
- `pkg/s3client`: all S3 persistence.
- `pkg/sprobot`: shared types such as templates.
- `pkg/testutil`: fake S3 and a test client.

## Build, test, run

```
go build ./... && go vet ./... && go test ./...   # what CI runs, plus a gofmt check
./test.sh                                         # same thing inside the Docker `test` target
./run.sh | ./run-sticky.sh | ./run-thread.sh | ./run-web.sh   # one bot in dev mode
./run-all.sh                                      # everything via docker-compose.dev.yml
```

- **Formatting:** code must be `gofmt`-clean. The CI test image fails on
  `gofmt -l .` output.
- **Config:** `config/config.env`, copied from `config/example.env`. It holds
  the bot tokens and S3 credentials. Never commit it, and never print tokens
  from it.

## CI and deploy

- `.github/workflows/test.yaml` runs the Docker `test` target on amd64 and
  arm64 for PRs and non-default-branch pushes. `depcheck.yaml` runs dependency
  review on PRs.
- `.github/workflows/publish.yaml` runs on pushes to `main` and a Mon/Wed/Fri
  cron. It calls the tests, then builds and pushes the multiarch images
  `sadbox/sprobot`, `sadbox/sprobot-web`, `sadbox/stickybot` and
  `sadbox/threadbot`.
  - **Merging to `main` publishes images.** Treat a merge as a release.
- Dependabot opens weekly dependency PRs. A scheduled agent reviews and merges
  them.

## Conventions

- **Per-guild configuration is hardcoded in Go maps keyed by guild ID.** For
  example, `getTicketConfig` in `pkg/bot/tickets.go`.
  - Prod guild: `726985544038612993`. Dev guild: `1013566342345019512`.
  - When adding guild config, add **both** entries, and keep the tests that
    assert both exist passing.
- **Runtime state lives in S3, per guild.** In sprobot, use
  `guildStateStore[T]` (`pkg/bot/guildstore.go`): load on ready, persist on
  change and/or through `botutil.RunSaveLoop`, and save on shutdown. Missing a
  step loses state across restarts.
- **sprobot's disgo caches are persisted** to `/sprobot-cache/*.json`
  (`pkg/bot/groupedcache.go`) with size caps. The message cache also has a
  30-day retention limit. See the compliance rules below before touching it.
- **Slash commands are registered per guild**, not globally.
- **Component custom IDs** for sprobot are routed in
  `handleComponentInteraction` (`pkg/bot/deleteprofile.go`).
  - A new button needs a route there.
  - Changing a custom ID must keep the old one working: buttons already posted
    in Discord keep their old IDs forever.
- **Shared state** is guarded by a mutex on the state struct. Copy what you need
  under the lock, release it, then make Discord/S3 calls.
- **Tests:** keep logic in pure functions (formatting, parsing, custom-ID
  encoding) and unit-test those. Handlers that need a live Discord session are
  verified manually in the dev guild.
- Match the surrounding code's comment density and naming. Don't add
  boilerplate.

## Pull requests

- Branch off an up-to-date `origin/main`. PRs are squash-merged.
- PR body: **What** (the change), **Why** (the reason), **Testing** (how it was
  verified). Keep it short, and don't walk through the code file by file.
- State plainly what was **not** verified (for example "not exercised in a live
  guild").

## Discord privileged intents: compliance rules

Discord's privileged-intent approvals are **renewed annually** (next around
August 2027). Current status, the reasoning behind it, and the renewal
checklist are in [`docs/privileged-intents/`](docs/privileged-intents/README.md).
Read that README before changing anything below.

Current approved set:
- **sprobot:** Server Members + Message Content.
- **threadbot:** Server Members (REST List Thread Members only).
- **stickybot:** none.
- **No bot uses Presence.**

**Changes in these categories need explicit approval from the maintainer before
they are implemented.** Stop and ask, saying which bot and intent are affected
and why no intent-free alternative works:

1. **Adding a privileged intent to any bot**, either on the gateway
   (`gateway.WithIntents`) or by depending on its Developer Portal toggle.
   Watch out for the REST-only cases:
   - `GetMessage`/`GetMessages` on other users' messages needs Message Content.
   - List/Search Guild Members and List Thread Members need Server Members.
2. **Using an approved intent for a new purpose**, i.e. any new feature that
   reads other users' message content, embeds or attachments, or relies on
   member events or the member cache. The approvals were granted for the uses
   described in `docs/privileged-intents/`. A new use must be added there and
   may need to be disclosed at renewal.
3. **Storing or retaining more user data**: new persisted fields, new caches,
   new S3 data, longer retention, or anything that weakens the 30-day message
   retention (`messageRetention`) or the encryption-at-rest claim. Encryption
   at rest comes from the production host's encrypted disk, not from the app,
   so moving `/sprobot-cache` to another host or volume counts here too.
4. **Moving data off our infrastructure or to a new processor** (sharing,
   analytics, AI processing beyond what `PRIVACY.md` describes).

Changes that **align with compliance** and need no special approval:
- Removing a dependency on an intent.
- Reading data from exempt sources:
  - the bot's own messages
  - interaction payloads
  - message context-menu resolved targets (see stickybot's stash pattern in
    `docs/privileged-intents/stickybot.md`)
  - author/member fields that arrive without the intent
- Shrinking retention or the data stored.

Every change that touches intents or user data must, **in the same PR**:
- update the relevant `docs/privileged-intents/*.md` tables;
- update [`PRIVACY.md`](PRIVACY.md) if what is collected, why, for how long, or
  who processes it has changed;
- when dropping an intent, plan the rollout: deploy first, **then** turn the
  portal toggle off.
