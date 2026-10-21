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
| "Save message to mod log" context menu | Content (split into fields) and attachments (copied to S3) of the target message | `handleModLogModalSubmit`, `pkg/bot/modlog.go` |

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

1. **Mod-log save re-fetches through REST.** `handleModLogMenu` already has the
   exempt resolved target (`data.TargetMessage()`), but only its IDs are
   carried to the modal submit, which then calls `Rest.GetMessage`. Using
   stickybot's stash pattern ([stickybot.md](stickybot.md)) would remove one
   Message Content dependency. It wouldn't remove the intent, since the event
   log still needs it.
2. **Persisted caches that nothing reads.** The thread-member and presence
   caches are written to disk but no sprobot feature reads them. Dropping them
   is cheap data minimisation and simplifies the review answers.
3. **Retention of non-message caches.** Only the message cache has a time
   limit. The member, thread-member and presence caches are size-capped but
   otherwise persist indefinitely. `PRIVACY.md` doesn't promise otherwise, but
   expect the question.
4. **Temp-role detection overlap.** Role adds also arrive as audit-log
   `MemberRoleUpdate` entries, and `checkTempRolesOnMessage` covers active
   users. Detection could probably move off `GUILD_MEMBER_UPDATE`, although the
   nickname log and the join/leave/welcome features would still keep Server
   Members.
