# sprobot: Server Members + Message Content

**Status:** both intents were applied for on 2026-08-29 and approved.

Gateway intents (`pkg/bot/bot.go`): Guilds, **GuildMembers**, GuildModeration,
GuildMessages, GuildMessageReactions, **MessageContent**, GuildExpressions,
GuildVoiceStates, GuildScheduledEvents. Presence is not requested, and member
chunking is off (no `WithMemberChunkingFilter`).

sprobot-web (`cmd/sprobot-web`) makes no Discord API calls. It serves profiles
from S3 only.

## Message Content

### Features that need it

| Feature | What it reads | Where |
|---|---|---|
| Event log: message deleted | Cached content and author of the deleted message. Delete events carry no content, so this relies on the message cache (below). | `onMessageDelete`, `pkg/bot/eventlog.go` |
| Event log: message edited | Old content (from cache) vs new content (`MESSAGE_UPDATE`), shown as Before/After. Without the intent both sides would be empty and every edit log would be dropped. | `onMessageUpdate`, `pkg/bot/eventlog.go` |
| Starboard | Content and first image attachment of the starred message, fetched with `Rest.GetMessage` after a reaction | `postStarboardEntry`, `pkg/bot/topreactions.go` |
| "Save message to mod log" context menu | Content (split into fields) and attachments (copied to S3) of the target message. Read from the exempt context-menu resolved target (`botutil.MessageStash`). Only a modal submitted across a bot restart falls back to `Rest.GetMessage`, which needs the intent. | `handleModLogModalSubmit`, `pkg/bot/modlog.go` |

### The message cache

The event log needs the content of a message as it was *before* an edit or
delete, so sprobot caches messages:
- disgo's message cache is backed by sprobot's capped cache (up to 1M
  messages), filled from `MESSAGE_CREATE`.
- It is persisted to `/sprobot-cache/messagecache.json` every 5 minutes and on
  shutdown, so the event log survives restarts.
- **Encryption at rest:** provided by the production host's full-disk
  encryption (confirmed by the maintainer, 2026-10-21), not by the application.
  The cache files themselves are plain JSON (`os.WriteFile`, mode `0o644`).
  This is the "encrypted at rest" claim in `PRIVACY.md` and the review form, so
  any move to a new host or volume must keep disk encryption, or add
  application-level encryption first.
- **Retention:** `messageRetention = 30 days` (`pkg/bot/groupedcache.go`).
  `pruneMessageCache` drops messages older than that, judged by snowflake time.
  It runs on load and hourly. This is the "≤ 30 days" claim certified in the
  review (PR #223).
- Because pruning is hourly, the file on disk can hold an expired message for
  up to about an hour past 30 days.

### Features that do **not** need it (exempt or content-free)

- **Bot's own messages:** ticket panel, self-role panel and mod-log thread
  upkeep, link migration rewrites (filtered to the bot's own author ID), and
  ticket opener recovery from the intro embed on close.
- **Author/member fields only.** These arrive without the intent:
  - top-poster counts
  - profile display names from messages (`pkg/bot/profilenames.go`)
  - auto-role on first message
  - temp-role detection on message
  - poster-role counting
  - forum "nobody replied" reminders
- **Interaction data:** shortcuts (`/s`), the "Get profile" context menu (uses
  the target's author), `/warn`.
- **Market progress** uses guild message search with `author_id` and parses
  only `channel_id` and `total_results`. No content is read.

## Server Members

Member chunking is off and nothing calls List/Search Guild Members, so the
dependency is entirely on the **member events** and the member cache they fill.

| Feature | Depends on | Where |
|---|---|---|
| Member-join log, welcome DM, auto-role on join | `GUILD_MEMBER_ADD` | `onMemberJoin`, `pkg/bot/autorole.go`; `pkg/bot/welcome.go` |
| Member-leave log | `GUILD_MEMBER_REMOVE` | `onMemberLeave`, `pkg/bot/eventlog.go` |
| Nickname-change log | `GUILD_MEMBER_UPDATE` + cached previous member | `onMemberUpdate`, `pkg/bot/eventlog.go` |
| Temp role detected on role add | `GUILD_MEMBER_UPDATE` | `checkTempRolesOnMemberUpdate`, `pkg/bot/temprole.go` |
| Temp-role expiry mod-log post | Member cache, for the user. The role removal itself doesn't depend on it, but the post is skipped if the member isn't cached. | `pkg/bot/temprole.go` |
| `/topposters` role filter and names | Member cache. Degrades to stored usernames and no role filter. | `pkg/bot/topposters.go` |

The member cache is persisted to `/sprobot-cache/membercache.json` (size-capped
at 100k, **no time-based expiry**), mainly so the previous-member value
survives restarts.

### Features that do **not** need it

- Moderation actions (ban, kick, timeout, role updates) come from audit-log
  events (GuildModeration) plus `Rest.GetUser`.
- Interaction member data (permissions, roles, nickname) is always included.
- `/getprofile` and `/market progress` call Get Guild Member for one member,
  which is not intent-gated.

## Open items to resolve before the next renewal

1. **Member cache entries filed under thread IDs (disgo bug).**
   - disgo's `THREAD_MEMBERS_UPDATE` handler (`bot/handlers/thread_handler.go`
     in disgo v0.19.6) sets `Member.GuildID` to the *thread* ID before adding
     the member to the member cache.
   - Removal on `GUILD_MEMBER_REMOVE` uses the real guild ID, so those entries
     are never cleared. Departed users' member data can stay in
     `membercache.json` until the 100k cap evicts it.
   - Fix by pruning member entries whose group key isn't a known guild ID
     (on load and periodically), or by patching disgo upstream.
2. **Retention of the member cache.** Only the message cache has a time limit.
   Members are removed when they leave the server (apart from item 1), so the
   cache holds roughly the current membership. `PRIVACY.md` doesn't promise
   otherwise, but expect the question.

### Resolved

- **Mod-log save re-fetched through REST.** PR #248: it now uses the
  context-menu resolved target via `botutil.MessageStash`, and falls back to
  REST only across a restart. This also fixed losing the evidence when the
  message was deleted while the mod wrote the note.
- **Persisted caches nothing read.** PR #249: the presence and thread-member
  caches were removed, and their files are deleted on startup.
  - The enabled caches are now an explicit list (`enabledCacheFlags`), and a
    test fails until any new disgo cache is classified.
  - The thread-member cache had about 8k memberships across 673 threads
    before removal.
- **Temp-role detection overlap.** Dropped. Moving detection to audit-log
  entries wouldn't remove Server Members, since the join/leave/welcome and
  nickname features need it. It would only add REST calls.
