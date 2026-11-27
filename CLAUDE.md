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
  `GuildAllowed`), the `/healthz` server, gateway-close handling, `RunSaveLoop`
  and `RunJobLoop`, the bots' telemetry (`telemetry.go`),
  `PostWithRetry`, `FollowupEphemeral`, `MessageStash`, `RegisterGuildCommands`,
  `PrivacyPolicyStatus`.
- `pkg/idleloop`: the idle-repost loop used by stickybot and threadbot.
- `pkg/envconfig`: reads and checks each program's whole environment at
  startup.
- `pkg/logsetup`: one log format for every program (slog text, stderr).
- `pkg/buildinfo`: which build a program is (`Version`, the short commit
  SHA the Dockerfile links in): what `-version` prints and telemetry sends
  as `service.version`.
- `pkg/s3client`: all S3 persistence.
- `pkg/statever`: persisted-state versions and `-check-state`.
- `pkg/sprobot`: shared types such as templates.
- `pkg/testutil`: fake S3 and a test client.
- `pkg/telemetrytest`: records spans and metrics in memory for tests.

`cmd/s3-orphans` is a maintenance tool, not a bot. It reports (and with
`-delete` removes) bucket objects that no profile, sticky or shortcut refers to
any more. It never touches `mod_files/`.

## Build, test, run

```
go build ./... && go vet ./... && go test -race ./...   # what CI runs, plus a gofmt check
./test.sh                                               # same thing inside the Docker `test` target
./run.sh | ./run-sticky.sh | ./run-thread.sh | ./run-web.sh   # one bot in dev mode
./run-all.sh                                            # everything via docker-compose.dev.yml
```

- **Formatting:** code must be `gofmt`-clean. The CI test image fails on
  `gofmt -l .` output.
- **Config:** `config/config.env`, copied from `config/example.env`. It holds
  the bot tokens and S3 credentials. Never commit it, and never print tokens
  from it.
  - Every environment variable is read once, by `pkg/envconfig`, first thing
    in `main` (after `healthcheck.Main` and `-check-state`, which reads only
    the storage variables): before anything connects to Discord or S3 or
    writes a file. It checks every variable and, on any problem, logs each
    one by name (never a value) and exits 1. Programs get the checked values
    as a struct and read no variable themselves (the one exception:
    `sprobot-web -healthcheck` reads `PORT` before the check; and the
    `OTEL_*` variables, which the OpenTelemetry SDK reads itself, see
    Telemetry below). A new variable goes there, with a check.
  - Every value is plain; the binaries never talk to 1Password. In prod the
    host renders secrets before the container starts (homelab-configs
    `quadlet/render-env.sh`); in dev, `run*.sh` resolve `config.env`'s
    `op://vault/item/field` references with `op run` (your own 1Password
    account, desktop app) and pass plain values into the containers. A value
    still starting with `op://` is refused by envconfig as a problem naming
    the variable, quoting nothing past the prefix.

## CI and deploy

- `.github/workflows/test.yaml` calls sadbox/ci's `go.yml`, which builds the
  Docker `test` target on amd64 and arm64 and runs gofmt, go vet,
  `go test -race`, golangci-lint (sadbox/ci's default configuration) and
  govulncheck inside it (checks `go / test (linux/amd64)` and
  `go / test (linux/arm64)`). It runs on pull requests, on pushes to
  non-default branches (Dependabot branches are excluded; they are tested
  through their PR), and when `publish.yaml` calls it. A newer run for the
  same PR or branch cancels the older one. PRs also build the four prod
  images for amd64 (not pushed, job `prod images (linux/amd64)`), to catch a
  broken prod stage before merge. `depcheck.yaml` runs dependency
  review on PRs.
- `.github/workflows/publish.yaml` runs on pushes to `main` (and by hand,
  `workflow_dispatch`, which always builds). Unless only docs (`*.md`,
  `docs`, `LICENSE`) changed since the commit `:latest` was built from, it
  calls the tests, then builds per-arch images and combines them into the
  multiarch images `ghcr.io/espresso-aficionados/{sprobot,sprobot-web,
  stickybot,threadbot}`, pushed with the workflow token.
  - Runs are serialized (one publish at a time, none cancelled once running).
  - `:latest` moves only if the commit is still the tip of `main`, checked once
    per run for all four images.
  - Every pushed image also gets a write-once `sha-<short>` tag (never a bare
    `<short>`). A rebuild of a commit that already has one (a re-run or a
    manual run) is tagged `sha-<short>-<UTC time>` instead. Images carry the
    `org.opencontainers.image.revision` label.
  - Every image carries BuildKit provenance (`mode=max`) and an SBOM, and the
    `attest` job adds a GitHub build-provenance attestation, pushed to GHCR,
    for each multi-arch index (`gh attestation verify
    oci://ghcr.io/espresso-aficionados/<image>:<tag> -R
    Espresso-Aficionados/sprobot`).
  - **Merging to `main` publishes images, and production runs `:latest`**
    with podman-auto-update, which restarts each bot on its new image (and
    rolls back one that never turns healthy). Treat a merge as a release.
- **Runtime images** are distroless `static-debian12:nonroot`, pinned by
  digest: uid 65532, no shell, no package manager. The container health
  check is each binary's own `-healthcheck` flag (`github.com/sadbox/svc`
  `healthcheck.Main`, first in `main`), which asks the program's `/healthz`
  (the bots' on `127.0.0.1:8099`, 200 while the gateway is ready). The
  production units (Quadlet, in the private homelab-configs repo) run them
  read-only, with no capabilities and a user namespace of their own;
  sprobot's `/sprobot-cache` is `/var/lib/sprobot` on the host, owned by
  65532. A change must work with the unit as it is: its `HealthCmd`, its
  mounts, its environment.
- The Dockerfile builder is pinned to `golang:1.27.4` (tag and digest). The
  `go` directive in `go.mod` is `1.26.0`.
- `github.com/gorilla/websocket` (disgo's gateway) is pinned to an untagged
  commit, `v1.5.4-0.20240701034025-d67f41855da4`, for GO-2026-6278 (its
  v1.5.3 mask key comes from `math/rand`; govulncheck in go.yml fails on
  it). Dependabot never moves a pseudo-version: when gorilla tags a release
  past it, or disgo requires one, move to that tag by hand.
  `TestGatewayWebsocketThroughConnectProxy` keeps the gateway's dial through
  egress (a CONNECT proxy from `HTTPS_PROXY`) working.
- Dependabot opens daily dependency PRs, each version waiting out a 3-day
  cooldown. The dep-review gate (`dep-review.yaml`, sadbox/dep-review)
  reviews and merges what's safe; majors and anything it flags wait for a
  human.

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
  opens, persist on change and/or through a periodic save loop, and save on
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
  - **Persisted state is versioned** (`pkg/statever`, `statever.Current`).
    A document's version is its top-level `"version"`, first when present;
    none means version 1. Objects of fields (every `guildStateStore`,
    threadbot's member counts and hidden threads) are written with it
    (`statever.Stamp`), since older builds ignore it. Maps keyed by ID
    (stickies, thread reminders) and the cache files (JSON arrays) can't
    carry one without breaking older builds, so they are still written
    without one: they are read with or without it, and the cache dir's
    version is `/sprobot-cache/version.json`. State newer than the build
    is refused, never treated as corrupt: startup exits 1 before the
    gateway opens, and a later retry leaves the guild unloaded. Bump
    `Current` for any change an older build would misread or destroy by
    rewriting (see its comment), and with it start writing the version
    into the maps and cache files. Each binary's `-check-state [-json]`
    reads its state read-only (sprobot's `bot.CheckState` uses
    `stateStores` and `cacheFiles`; keep both complete) and exits 0, 3
    (newer) or 1 (unreadable).
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
- **Gateway health:** `/healthz` and the `gateway_ready` job (see Telemetry)
  report healthy only while the gateway is ready. A gateway close
  that disgo won't reconnect from (e.g. 4004 bad token, 4014 disallowed
  intents) makes the bot save, wait 10 minutes, and exit non-zero.
- **Gateway session handoff** (`pkg/botutil/session.go`, on with
  `GATEWAY_SESSION_DIR`): on SIGTERM a bot closes its gateway with 1012
  first (`HandOffGateway`), waits for traced handlers still running, does
  its saves, and writes `session.json` last (`SaveHandoff`); the next
  process takes it (single use, refused past 60s from the close, Discord's
  window being 60-120s) and resumes. A resume sends no READY or
  GUILD_CREATE and replays missed events *before* RESUMED, so:
  - whatever a bot does on GuildReady or READY that a resumed session
    still needs is seeded from `Resumed()` **before** the gateway opens
    (stickybot's ready guilds; threadbot's reminder loops and session
    start), and must be safe to repeat: a refused resume falls back to
    READY and GuildReady;
  - handlers for a *new* session only (modlog's `resetAll`, forum
    reminders' `forgetWaiting`, the deleted-channel checks) stay on
    READY/GuildReady: a resume replays what they'd make up for;
  - guilds out (unavailable or unready) at the handoff are marked unready
    again before the gateway opens (`preRegister`), as disgo's READY
    handler does: back from the outage, they dispatch GuildReady, not
    GuildJoin;
  - `GuildIDs()` adds the session's guilds (no guild cache on a resume),
    and sprobot resumes only when every cache file loaded and hands off
    only when every cache saved: its persisted caches are what a resume
    continues from;
  - a new event handler must go through `TraceEvent`/`Trace*`, which the
    handoff counts, so no event is half-handled when the state is saved;
  - an interaction replayed from the gap and already past Discord's 3s
    answer deadline is skipped (`traceInteraction`): its handler would act,
    fail to respond, and the user would likely try again;
  - every bot hands off only when all its shutdown saves succeeded: a
    resume continues from exactly what was saved.
  disgo logs a refused resume at ERROR ("failed to reconnect gateway");
  it identifies right after, and that line is expected.
- **Never log a secret** (tokens, S3 keys): envconfig's problems name the
  variable, never its value.
- **Telemetry** goes through `github.com/sadbox/svc/telemetry` (its README
  is the standard): metrics and traces over OTLP to the host's Alloy, off
  without `OTEL_EXPORTER_OTLP_ENDPOINT`. `telemetry.Start` runs only in a
  program's long-running mode, after `-healthcheck`, `-check-state` and
  envconfig (the bots: `botutil.StartTelemetry`), and a failure there only
  logs a warning. sprobot-web's whole handler chain is inside
  `telemetry.Handler` (routes are the mux patterns; a middleware must pass
  the request on, not an `r.WithContext` copy, or the route is lost). Every
  outbound client has a fixed peer name: `discord` (disgo's REST client,
  `botutil.RESTClient`, and sprobot's search client), `s3`, `fetch`
  (user-supplied URLs, wrapped outside the dial check; in production they
  are asked of the egress proxy's fetch gateway, `EGRESS_FETCH_GATEWAY`,
  which fetches them itself and hands back only an image), `discord-cdn`
  (Discord's own attachment URLs, for mod-log and sticky saves).
  logsetup's handler adds `trace_id` to `*Context` log calls made inside a
  span.
  - **Bots:** each periodic loop is a `telemetry.Job`, a package-level var
    (so it exists before `Start`), run by `botutil.RunJobLoop` (sprobot:
    `startLoop`) with the pass's real error; a callee that only logged its
    failures returns them too. The 30s heartbeat tick records
    `gateway_ready` while the gateway is ready: the bots' liveness signal,
    which Grafana alerts on. Interaction
    listeners go through `botutil.TraceCommands`/`TraceAutocomplete`/
    `TraceComponents`/`TraceModals`: labelled by registered command name, or
    by a custom-ID prefix from the bot's fixed list (sprobot's
    `componentNames`/`modalNames` in `pkg/bot/telemetry.go`; a new route's
    prefix goes there, else it is `other`). Gateway events are counted by
    disgo type (`botutil.CountEvents`).
  - **Feature counts** (profiles, members, starboard, tickets, stickies, reminders)
    are listed in each bot's `telemetry.go`: counters are a
    `botutil.Counter` (observable, its fixed series seeded so they read 0
    from the first export), gauges are read from state at export
    (`registerStats`, called in `Run`), summed over the guilds the instance
    serves and left out while one of them is unloaded.
  - **No Discord ID, name, message content, custom ID or request data in a
    span attribute or metric label**; label values are a small fixed set.
    The one exception is `sprobot_shortcut_uses_total{shortcut}`: a
    shortcut's name, staff-written config (`/config shortcuts`), counted
    only once it matched a configured shortcut, and capped at 50 names a
    process (the rest `other`). Test with `pkg/telemetrytest`.
- **Slash commands are registered per guild**, not globally.
- **Component custom IDs** for sprobot are routed in
  `handleComponentInteraction` (`pkg/bot/deleteprofile.go`).
  - A new button needs a route there, and its prefix in `componentNames`.
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
