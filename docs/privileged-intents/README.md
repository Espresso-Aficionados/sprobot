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
to threadbot's Server Members (since PR #251).

## Renewal checklist

1. **Re-verify the dependency tables** in each bot's doc against the code. At
   minimum, grep for `Intent[A-Z]`, `GetMessage(`, `GetMessages(`,
   `GetThreadMembers`, `SearchMembers` and `Caches().Member`. Update the docs
   where the code has moved.
2. **Confirm the claims the form asks you to certify**, and that
   [`PRIVACY.md`](../../PRIVACY.md) still matches the code:
   - Message content retention ≤ 30 days: `messageRetention` in
     `pkg/bot/groupedcache.go`.
   - Encryption at rest: the production host's disk is encrypted. The app
     writes the cache as plain JSON, so re-confirm this on the current host. See
     [sprobot.md](sprobot.md).
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
     History. threadbot also needs Send Messages in Threads.
5. Re-apply per bot. A bot with **no** privileged intents (stickybot) cannot
   submit an application: the form's Submit button stays disabled with nothing
   checked. That is expected and needs no action.

## Rules for changes

The repo's [`CLAUDE.md`](../../CLAUDE.md) lists which changes need explicit
maintainer approval because they affect this review. Keep both files in sync
when a dependency is added or removed.
