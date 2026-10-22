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
- `pkg/botutil`: `BaseBot`, the guild allowlist (`AllowedGuildIDs`,
  `GuildAllowed`), healthcheck pings, gateway-close handling, `RunSaveLoop`,
  `PostWithRetry`, `FollowupEphemeral`, `MessageStash`, `RegisterGuildCommands`,
  `PrivacyPolicyStatus`.
- `pkg/idleloop`: the idle-repost loop used by stickybot and threadbot.
- `pkg/s3client`: all S3 persistence.
- `pkg/sprobot`: shared types such as templates.
- `pkg/testutil`: fake S3 and a test client.

`cmd/s3-orphans` is a maintenance tool, not a bot. It reports (and with
`-delete` removes) bucket objects that no profile, sticky or shortcut refers to
any more. It never touches `mod_files/`.

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
  arm64. It runs on pull requests, on pushes to non-default branches
  (Dependabot branches are excluded; they are tested through their PR), and
  when `publish.yaml` calls it. A newer run for the same PR or branch cancels
  the older one. PRs from amd64 also build the four prod images (not pushed),
  to catch a broken prod stage before merge. `depcheck.yaml` runs dependency
  review on PRs.
- `.github/workflows/publish.yaml` runs on pushes to `main` and a Mon/Wed/Fri
  cron. It calls the tests, then builds per-arch images and combines them into
  the multiarch images `sadbox/sprobot`, `sadbox/sprobot-web`,
  `sadbox/stickybot` and `sadbox/threadbot`.
  - Runs are serialized (one publish at a time, none cancelled once running).
  - `:latest` moves only if the commit is still the tip of `main`, checked once
    per run for all four images.
  - Every pushed image also gets a write-once `sha-<short>` tag. A rebuild of a
    commit that already has one (a scheduled rebuild or a re-run) is tagged
    `sha-<short>-<UTC time>` instead. Images carry the
    `org.opencontainers.image.revision` label.
  - **Merging to `main` publishes images.** Treat a merge as a release.
- The Dockerfile builder is pinned to `golang:1.27.3`. The `go` directive in
  `go.mod` is `1.26.0`.
- Dependabot opens weekly dependency PRs. A scheduled agent reviews and merges
  them.

## Conventions

- **Per-guild configuration is hardcoded in Go maps keyed by guild ID.** For
  example, `getTicketConfig` in `pkg/bot/tickets.go`.
  - Prod guild: `726985544038612993`. Dev guild: `1013566342345019512`.
  - When adding guild config, add **both** entries, and keep the tests that
    assert both exist passing.
- **Only allowlisted guilds are served.** `botutil.AllowedGuildIDs` (prod and
  dev) is the list; `botutil.GuildAllowed` checks it. Commands, state, event
  handlers and caches act only on those guilds. A bot added to any other guild
  logs it and ignores it, and never leaves it. Interaction handlers call
  `RejectGuildNotAllowed` first. A new guild needs an entry in the allowlist
  and in every per-guild config.
- **Runtime state lives in S3, per guild.** In sprobot, use
  `guildStateStore[T]` (`pkg/bot/guildstore.go`): load before the gateway
  opens, persist on change and/or through `botutil.RunSaveLoop`, and save on
  shutdown (after the loops are drained). Missing a step loses state across
  restarts.
  - **Never overwrite data that failed to load.** A guild whose load failed has
    no state until a retry succeeds: `get` and `getOrCreate` return nil, `set`
    is refused and `persist`/`save` fail. Use `getOrCreate` (not `get` plus
    `set`) to obtain state, and handle nil by refusing the operation. Stored
    objects that fail to decode are backed up under `corrupt/` for 30 days,
    then replaced by defaults.
  - threadbot and stickybot follow the same rule with their own loaders: a
    guild is in memory only once its stored data loaded, and nothing is saved
    for a guild that never loaded.
- **sprobot's disgo caches are persisted** to `/sprobot-cache/*.json`
  (`pkg/bot/groupedcache.go`) with size caps that evict oldest first. Files are
  written atomically (temp file, fsync, rename) with mode `0600`. The message
  cache also has a 30-day retention limit: expired messages are pruned on load
  and before every save. It stores only the fields its readers use
  (`cachedMessage`). The message and member caches keep only allowlisted
  guilds, and keep a guild's entries through a guild outage. See the compliance
  rules below before touching it.
  - The enabled caches are an explicit list, `enabledCacheFlags`, never
    `cache.FlagsAll`. `TestCacheFlagsClassified` fails CI until every disgo
    cache flag is either persisted or listed in `disabledCacheFlags` with a
    reason. When a disgo upgrade trips it, the default answer is "disabled".
  - Flags only gate disgo's default caches. A custom cache passed with
    `cache.With*Cache` ignores them, so removing a cache means removing its
    option too.
- **Context-menu targets** are carried to modal submits with
  `botutil.MessageStash`, not re-fetched over REST (see
  `docs/privileged-intents/stickybot.md`). sprobot's mod-log save and
  stickybot's sticky creation both do this, with a REST fallback only across a
  restart.
- **Shared Discord helpers:** `botutil.PostWithRetry` posts with a nonce and
  `enforce_nonce` (so a retry can't duplicate) and does not retry 4xx.
  `botutil.FollowupEphemeral` answers a deferred interaction.
  `s3client.DeleteKeys` deletes S3 objects: anything that stores a file next to
  a record must delete the file when the record goes.
- **Event dispatch:** threadbot and stickybot handle events asynchronously
  (`bot.WithAsyncEventsEnabled`), because stopping a repost loop waits out a
  repost in flight (`idleloop.Handle.Stop` blocks until it ends, so never call
  it from inside the repost callback).
- **Gateway health:** the healthcheck pings only while the gateway is ready. A
  gateway close that disgo won't reconnect from (e.g. 4004 bad token, 4014
  disallowed intents) makes the bot save, wait 10 minutes, and exit non-zero.
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
- **threadbot:** Server Members, for exact thread member counts only: the List
  Thread Members REST endpoint (paged, `with_member=true`; members are counted
  and discarded), plus thread member add/remove events on the gateway. Only a
  count and a fetch time per thread is stored, never a member.
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
   retention (`messageRetention`), the served-guilds-only rule for the caches,
   or the encryption-at-rest claim. Encryption
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
  who processes it has changed. That includes its "Moderator-private data"
  section (mod-log records, saved attachments, the in-memory user-to-thread
  index) and what the starboard reposts;
- when dropping an intent, plan the rollout: deploy first, **then** turn the
  portal toggle off.
