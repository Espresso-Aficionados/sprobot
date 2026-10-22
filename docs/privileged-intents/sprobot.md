# sprobot: Server Members + Message Content

**Status:** both intents were applied for on 2026-08-29 and approved.

Gateway intents (`pkg/bot/bot.go`): Guilds, **GuildMembers**, GuildModeration,
GuildMessages, GuildMessageReactions, **MessageContent**, GuildExpressions,
GuildVoiceStates, GuildScheduledEvents. Presence is not requested, and member
chunking is off (no `WithMemberChunkingFilter`).

sprobot serves only the guilds in `botutil.AllowedGuildIDs` (prod and dev).
Commands, state, handlers and the caches below act on those guilds only; a
guild outside the list is logged and ignored, and the bot never leaves it.

sprobot-web (`cmd/sprobot-web`) makes no Discord API calls. It serves profiles
from S3 only.

## Message Content

### Features that need it

| Feature | What it reads | Where |
|---|---|---|
| Event log: message deleted | Cached content and author of the deleted message. Delete events carry no content, so this relies on the message cache (below). | `onMessageDelete`, `pkg/bot/eventlog.go` |
| Event log: message edited | Old content (from cache) vs new content (`MESSAGE_UPDATE`), shown as Before/After. Without the intent both sides would be empty and every edit log would be dropped. | `onMessageUpdate`, `pkg/bot/eventlog.go` |
| Starboard | Content and first image attachment of the starred message, fetched with `Rest.GetMessage` once the reaction count reaches the threshold. Reaction counts (here and when updating a post) come from the message's `reactions`, which need no intent. By maintainer decision the starboard does not compare who can see the source channel with who can see the starboard: it skips private threads, channels and categories on the starboard filter list, and age-restricted sources unless the starboard is age-restricted, so staff-only channels must be on the filter list. The repost is deleted when the original is deleted (seen live, or when a channel or thread is deleted). `PRIVACY.md` says the same. | `createStarboardPost`, `pkg/bot/topreactions.go` |
| "Save message to mod log" context menu | Content (split into fields) and attachments (copied to S3) of the target message. Read from the exempt context-menu resolved target (`botutil.MessageStash`, held in memory while the modal is open). Only a modal submitted across a bot restart falls back to `Rest.GetMessage`, which needs the intent. The user's thread is found by the user ID parsed from the thread name, through an in-memory user-to-thread index (IDs and legacy thread names only, never persisted; disclosed in `PRIVACY.md` under "Moderator-private data"). Content is split on character boundaries and the embed is capped at 6000 characters. | `handleModLogModalSubmit`, `pkg/bot/modlog.go` |

### The message cache

The event log needs the content of a message as it was *before* an edit or
delete, so sprobot caches messages:
- disgo's message cache is backed by sprobot's capped cache (up to 1M
  messages, oldest evicted first), filled from `MESSAGE_CREATE`.
- **Only served guilds:** messages from a guild outside `AllowedGuildIDs`, and
  DMs, are not cached (`outageSafeMessageCache`). A guild's messages are also
  kept through a guild outage rather than dropped by disgo's `GUILD_DELETE`
  handling.
- **Slim entries:** the cache stores `cachedMessage`, not the whole
  `discord.Message`. It keeps only what the readers use: IDs, type, content,
  the author's ID, name, avatar and bot/system flags, webhook ID, edit time,
  message reference and components. Embeds, attachments, mentions and
  reactions are not kept. A new reader of cached messages must check those
  fields are enough.
- It is persisted to `/sprobot-cache/messagecache.json` every 5 minutes and on
  shutdown, so the event log survives restarts. The file is written
  atomically (temp file, fsync, rename) with mode `0600`, and streamed rather
  than held in memory as one JSON document. If a save fails, the old file is
  deleted so it cannot outlive the retention limit.
- **Encryption at rest:** provided by the production host's full-disk
  encryption (confirmed by the maintainer, 2026-10-21), not by the application.
  The cache files are plain JSON, readable by the owner only. This is the
  "encrypted at rest" claim in `PRIVACY.md` and the review form, so any move to
  a new host or volume must keep disk encryption, or add application-level
  encryption first.
- **Retention:** `messageRetention = 30 days` (`pkg/bot/groupedcache.go`).
  Messages older than that, judged by snowflake time, are dropped on load
  (the file is rewritten at once if any were) and by `pruneMessageCache`, which
  `saveCaches` runs before every save. This is the "≤ 30 days" claim certified
  in the review (PR #223). The file on disk therefore holds an expired message
  for at most one save interval (5 minutes), and nothing prunes it while the
  bot is down.

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
at 100k, oldest evicted first, **no time-based expiry**), mainly so the
previous-member value survives restarts. It keeps only members of served guilds
(`guildMemberCache`), which also drops the entries disgo files under thread IDs
on `THREAD_MEMBERS_UPDATE`. A guild's members are kept through a guild outage.
The file is written like the message cache (atomic, mode `0600`).

### Features that do **not** need it

- Moderation actions (ban, kick, timeout, role updates) come from audit-log
  events (GuildModeration) plus `Rest.GetUser`.
- Interaction member data (permissions, roles, nickname) is always included.
- `/getprofile` and `/market progress` call Get Guild Member for one member,
  which is not intent-gated.

## Open items to resolve before the next renewal

1. **Retention of the member cache.** Only the message cache has a time limit.
   A member is removed when the bot sees them leave the server, so the cache
   holds roughly the current membership, but a member who leaves while the bot
   or the server is offline stays cached. `PRIVACY.md` says this ("It has no
   time limit"), but expect the question at renewal.

### Resolved

- **Member cache entries filed under thread IDs (disgo bug).** PR #255: the
  member cache now accepts only members of served guilds, so the thread-ID
  entries disgo's `THREAD_MEMBERS_UPDATE` handler adds are never stored, and
  any already in `membercache.json` are dropped on load. Guilds outside the
  allowlist and DMs are likewise kept out of the message cache.
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
- **Cache hardening.** PR #255: atomic `0600` writes, expired messages pruned
  before every save and on load (previously hourly), slimmer message entries,
  served-guilds-only caches, and caches kept through a guild outage.
- **Temp-role detection overlap.** Dropped. Moving detection to audit-log
  entries wouldn't remove Server Members, since the join/leave/welcome and
  nickname features need it. It would only add REST calls.
