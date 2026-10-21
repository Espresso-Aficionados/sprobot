# threadbot: Server Members only

**Status:** Server Members was applied for and approved. Message Content has
never been used.

Gateway intents (`pkg/threadbot/bot.go`): `IntentGuilds`, `IntentGuildMessages`.
`IntentGuildMembers` is deliberately **not** declared on the gateway. The
privileged access threadbot needs is REST-only.

## Why Server Members

Reminders and `/threads` show a member count per thread.
- Discord's thread `member_count` field **caps at 50**.
- To show exact counts, `countThreadMembers` (`pkg/threadbot/reminders.go`)
  calls List Thread Members (`Rest.GetThreadMembers`) and counts the result.
- Discord's docs say of that whole endpoint: *"This endpoint is restricted
  according to whether the `GUILD_MEMBERS` Privileged Intent is enabled for
  your application."* The restriction covers the endpoint itself, not just the
  `with_member=true` variant.
- Counts are cached per guild and persisted to S3. `/threadbot refreshcounts`
  forces a refresh.

**Graceful degradation:** if the intent is revoked, `countThreadMembers` gets
an error and returns 0. The reminder code only uses a cached count when it is
above 0, otherwise it falls back to `member_count`. Counts above 50 would show
as 50, and nothing else breaks.

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

## Why Message Content was never needed

- `onMessage` (`pkg/threadbot/bot.go`) checks `Author.Bot` and uses the message
  only as an activity signal for the idle-repost loop.
- Reminders are built from thread metadata (`MessageCount`, `MemberCount`,
  creation time) returned by the active-threads API call.
- Nothing reads `.Content`, embeds or attachments from other users' messages.

## Future risk

Discord's docs warn that List Thread Members will **always return paginated
results starting in API v11**. When disgo moves to v11, the single-call
approach from `4edc5b8` goes away and the rate-limit problem may come back.
Re-check this whenever disgo's API version changes.
