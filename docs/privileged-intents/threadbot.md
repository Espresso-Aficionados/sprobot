# threadbot: Server Members only

**Status:** Server Members was applied for and approved. Message Content has
never been used.

Gateway intents (`pkg/threadbot/bot.go`): `IntentGuilds`, `IntentGuildMessages`,
`IntentGuildMembers`. Only guilds are cached (`cache.FlagGuilds`), so the member
and thread-member events the intent delivers are counted, never stored.

## Why Server Members

Reminders and `/threads` show a member count per thread. The intent is used for
that one purpose, in two ways.

Discord's thread `member_count` field **caps at 50**. Below 50 it is shown as is,
and no privileged access is used.

For a **displayed** thread at the cap, threadbot needs an exact count:
1. **REST: List Thread Members** (`countThreadMembers`,
   `pkg/threadbot/reminders.go`) lists the thread and counts the result.
   - Discord's docs say of that whole endpoint: *"This endpoint is restricted
     according to whether the `GUILD_MEMBERS` Privileged Intent is enabled for
     your application."* The restriction covers the endpoint itself, not just
     the `with_member=true` variant.
2. **Gateway: thread member add/remove events** (`onThreadMemberAdd`,
   `onThreadMemberRemove`, PR #251) adjust the cached count by ±1 between
   fetches.
   - Discord sends these for other users only when `GUILD_MEMBERS` is in the
     identify bitmask.
   - They arrive for threads the bot can see but has **not** joined. Discord
     doesn't document this, so it was checked on 2026-10-21 against sprobot's
     old thread-member cache: other users' joins were recorded in 662 threads,
     sprobot itself appeared in only 31, and a spot-checked thread did not
     list sprobot as a member.

**What is stored:** one integer count and one fetch time per thread
(`threadmembercounts/<guild>.json` in S3). No user IDs, member objects or join
times. Counts not re-fetched for 7 days are pruned.

## Refresh design

- A thread at the cap with no cached count is fetched the first time it is
  shown. That covers new threads, threads that reach 50, and pruned entries.
  `TestThreadEmbedFetchesNewAndUncachedThreads` covers this.
- A failed fetch isn't recorded, so the next display retries it.
- **Expiry:**
  - 24h for counts fetched before the current gateway session started.
  - 7 days for counts fetched during it. An unbroken session means every join
    and leave event has been applied: a resume replays missed events, but a
    new session does not. The session start is recorded on `events.Ready`.
- Each full refresh logs `Thread member count drift` (cached vs fetched, and
  whether the count was live). Persistent nonzero drift on live counts would
  mean Discord skips events for some changes, for example auto-joins by
  posting, or members leaving the server. Neither is documented.
- List Thread Members is ordered by user ID, not join time, so there is no
  watermark for fetching only new members. Each refresh lists the whole thread.
- `/threadbot refreshcounts` clears the cache for a guild.

## Failure mode if the intent is revoked

`IntentGuildMembers` is in the identify bitmask. **If the portal toggle is
turned off, Discord refuses the gateway connection (close code 4014,
disallowed intents) and threadbot goes fully offline.** This is not the graceful
fallback the REST-only design had.

To drop the intent, follow the deploy-then-toggle order:
1. Ship a build without `IntentGuildMembers` and without the REST counts.
2. Then turn the toggle off.

## History

- `f64a33a` (2026-02-22): created with `Guilds` + `GuildMessages`.
- `d3c2ec5` (2026-02-24): added `IntentGuildMembers` for
  `GetThreadMembersPage(with_member=true)`.
- `4edc5b8` (2026-02-25): switched to unpaginated `GetThreadMembers` (the
  paginated calls hit rate limits) and removed `IntentGuildMembers` from the
  gateway.
  - The commit message claims this avoided the privileged intent. **That is
    wrong.** The endpoint is still gated by the portal toggle, and exact counts
    kept working only because the toggle stayed on.
- PR #250 (2026-10-21): exact counts only for displayed threads at the cap,
  with per-thread refresh times. Previously every active thread was fetched
  before the list was trimmed, which was 393 threads for `/threads`.
- PR #251 (2026-10-21): `IntentGuildMembers` back on the gateway for thread
  member events, which keep counts current. Approved explicitly by the
  maintainer.

## Why Message Content was never needed

- `onMessage` (`pkg/threadbot/bot.go`) checks `Author.Bot` and uses the message
  only as an activity signal for the idle-repost loop.
- Reminders are built from thread metadata (`MessageCount`, `MemberCount`,
  creation time) returned by the active-threads API call.
- Nothing reads `.Content`, embeds or attachments from other users' messages.

## Future risk

Discord's docs warn that List Thread Members will **always return paginated
results starting in API v11**. When disgo moves to v11, a thread with n members
costs ceil(n/100) calls. That only applies to displayed threads at the cap, at
most once a day each. Re-check this whenever disgo's API version changes.
