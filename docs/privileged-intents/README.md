# Discord privileged intents

Discord requires bots that reach more than 10k users to apply for each
privileged intent they use, and to **re-apply every year**. The first review
was submitted on 2026-08-29. Expect the next one around **August 2027**.

This folder records what each bot uses, why, and the design decisions made to
keep that set small. The goal is to make the renewal a check, not a fresh
investigation.

| Bot | Discord app ID | Server Members | Message Content | Details |
|---|---|---|---|---|
| sprobot | 769290355572867082 | Approved | Approved | [sprobot.md](sprobot.md) |
| threadbot | 1474854517236302015 | Approved (REST + gateway events) | Not used | [threadbot.md](threadbot.md) |
| stickybot | 1474049801078181888 | Not used | Not used (dropped 2026-08) | [stickybot.md](stickybot.md) |

Presence is not used by any bot, and nothing should add it.

## How Discord enforces intents

There are two independent gates. Most mistakes come from treating them as one.

1. **The gateway intent bitmask** (`gateway.WithIntents(...)` in each bot's
   `bot.go`). This controls which gateway events are delivered. Without
   `IntentGuildMembers` there are no member add, update or remove events and no
   member chunking. Without `IntentMessageContent`, message events arrive with
   empty content, embeds, attachments and components.
2. **The Developer Portal toggle.** This also gates **REST responses**, whatever
   the bot declares on the gateway.
   - Without Message Content, `GET /channels/{id}/messages[/{id}]` returns other
     users' messages with content, embeds and attachments blanked.
   - Without Server Members, List Guild Members, Search Guild Members and
     **List Thread Members** are restricted.

   Dropping an intent from the gateway bitmask is therefore not enough. Every
   REST call that depends on it must go too.

**Exempt from Message Content:**
- The bot's own messages.
- Messages that mention the bot, and DMs to the bot.
- The **resolved target of a message context-menu command**
  (`data.TargetMessage()`). This is what made stickybot's refactor possible.

Interaction payloads always carry the invoking `member`/`user`. That needs no
intent either.

**Ordering lesson.** When dropping an intent, deploy the code that stops
depending on it **first**, then turn the portal toggle off. Turning the toggle
off first breaks the REST paths immediately.

It is worse for an intent in a bot's identify bitmask. Discord then refuses the
gateway connection outright (close code 4014, disallowed intents), and the bot
goes offline. That applies to sprobot's Server Members and Message Content, and
to threadbot's Server Members (since PR #251). A bot whose gateway closes for
good (4014 included) saves its state, waits 10 minutes, then exits non-zero
instead of sitting offline silently.

## Renewal checklist

1. **Re-verify the dependency tables** in each bot's doc against the code. At
   minimum, grep for `Intent[A-Z]`, `GetMessage(`, `GetMessages(`,
   `GetThreadMembers` (also matches `GetThreadMembersPage`), `SearchMembers` and
   `Caches.Member(`. Also check the cache types in
   `pkg/bot/groupedcache.go` (`cachedMessage`, `guildMemberCache`,
   `outageSafeMessageCache`) and `enabledCacheFlags`. Update the docs where
   the code has moved.
2. **Confirm the claims the form asks you to certify**, and that
   [`PRIVACY.md`](../../PRIVACY.md) still matches the code:
   - Message content retention ≤ 30 days: `messageRetention` in
     `pkg/bot/groupedcache.go`, applied on load and before every save
     (`pruneMessageCache`).
   - Encryption at rest: the production host's disk is encrypted. The app
     writes the cache as plain JSON (mode `0600`), so re-confirm this on the
     current host. See [sprobot.md](sprobot.md).
   - The member cache has no time limit (a member is dropped when the bot sees
     them leave). `PRIVACY.md` says so; see the open item in
     [sprobot.md](sprobot.md).
   - `PRIVACY.md` has a "Moderator-private data" section covering mod-log
     records, saved attachments (not deleted automatically) and the in-memory
     user-to-thread index. Re-read it against `pkg/bot/modlog.go`.
   - Deletion and retention evidence: profile images and sticky and shortcut
     files are deleted with their records, and `go run ./cmd/s3-orphans`
     (report only; `-delete` removes) lists objects in the bucket that nothing
     refers to. `mod_files/` is never touched by it. Run it in report mode
     before renewal if the form asks about stored files.
   - The bots serve only `botutil.AllowedGuildIDs`; a guild outside it is
     ignored and never left.
   - A privacy policy is discoverable. Every bot sets
     `botutil.PrivacyPolicyStatus` as its custom status, and the portal links
     `PRIVACY.md` and `TERMS.md`.
3. **Refresh the evidence images** in [`../intent-evidence/`](../intent-evidence/)
   if the features they show have changed. Two of them are renderings of real
   API data made with headless Chrome. The member-join image is a real client
   screenshot.
4. **Portal install settings to keep:**
   - Discord Provided Link, scopes `bot` + `applications.commands`.
   - sprobot permissions integer 361045773440.
   - stickybot and threadbot: View Channels + Send Messages + Read Message
     History. threadbot's portal install settings also include Send Messages
     in Threads. The invite link each bot logs at startup (permissions 68608)
     doesn't include that, so the portal settings are the source of truth.
5. Re-apply per bot. A bot with **no** privileged intents (stickybot) cannot
   submit an application: the form's Submit button stays disabled with nothing
   checked. That is expected and needs no action.

## Rules for changes

The repo's [`CLAUDE.md`](../../CLAUDE.md) lists which changes need explicit
maintainer approval because they affect this review. Keep both files in sync
when a dependency is added or removed.
