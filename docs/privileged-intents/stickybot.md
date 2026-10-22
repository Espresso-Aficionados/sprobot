# stickybot: no privileged intents

**Status:** stickybot uses no privileged intents. Its Message Content portal
toggle is off and has stayed off since the PR #222 rollout (2026-08), with
sticky creation working throughout.

Gateway intents (`pkg/stickybot/bot.go`): `IntentGuilds`, `IntentGuildMessages`.
Only guilds are cached (`cache.FlagGuilds`).

stickybot serves only the guilds in `botutil.AllowedGuildIDs`; any other guild
is logged and ignored, and the bot never leaves it. Events are dispatched
asynchronously (`bot.WithAsyncEventsEnabled`), because stopping a sticky's
repost loop waits out a repost in flight and must not stall the gateway.

## History

- `50173ed` (2026-02-19): stickybot was created with `IntentMessageContent`.
- `8d04fe3` / PR #222 (2026-08-29): the intent was dropped.

## Why it once needed Message Content

The only real content read was in **"Sticky this message"** (message context
menu):

1. The context menu opens a configuration modal.
2. On modal submit, the old code re-fetched the target with `Rest.GetMessage`
   and copied its content, embeds and attachments into the sticky.

Without the Message Content portal toggle, that REST response comes back blank
for other users' messages.

## How it avoids the intent

A message context-menu interaction includes the target message as resolved
data, and **that copy is exempt from Message Content**. The catch is that the
sticky is created later, on modal submit, which is a separate interaction. So
the bot carries the resolved message across (`pkg/stickybot/commands.go`):

- **Stash:** `handleStickyMenu` stores `data.TargetMessage()` in a
  `botutil.MessageStash` (`pkg/botutil/stash.go`), an in-memory map keyed by
  message ID. sprobot's "Save message to mod log" uses the same type (PR
  #248).
- **Consume:** `handleStickyConfigModal` reads the stash first. It calls
  `Rest.GetMessage` only if the entry is missing, which means the bot
  restarted while the modal was open.
- **TTL:** entries are kept for `botutil.MessageStashTTL` (20 minutes), which is
  longer than the 15-minute modal token lifetime, so no valid submit can
  outlive its entry. Expired entries are pruned on every access.
- **Non-destructive reads:** reading doesn't remove the entry, so concurrent
  modals on the same message and duplicate submits all still resolve.
- **Accurate errors:** a stashed message that is genuinely empty gets "no text,
  embeds, or attachments". An empty REST response after a restart tells the
  user to run the context menu again, because that emptiness means Discord
  stripped the fields.
- **Known trade-off:** the sticky is a snapshot from when the menu was opened.
  Edits made while the modal is open are not picked up.

## What never needed the intent

- **Reposting** uses stickybot's own stored copy of the sticky's content
  (`pkg/stickybot/sticky.go`). The buffer and "only bots posted since" checks
  list recent messages but compare only message IDs and the author's bot flag.
- **`onMessage`** only uses incoming messages as an activity signal for the
  idle-repost loop. It never reads their content.
- The `/sticky` slash commands take their input from command options.

## Stored files and deleted channels

A sticky's attachments are copied to S3 (`sticky` files). They are deleted with
the sticky: `retireSticky` stops the repost loop, deletes the last posted
message, then deletes the files through `s3client.DeleteKeys`
(`deleteStickyFiles`). A failed delete is kept in memory and retried on the
save loop and at shutdown (`retryFileDeletes`). Only keys that parse as this
guild's sticky files are ever deleted.

A sticky whose channel or thread is deleted is removed the same way: live on
`GuildChannelDelete` and `ThreadDelete`, and on each `GuildReady`
(`dropStickiesOfDeletedChannels`), which checks each stored sticky's channel
with `GetChannel` and drops those that answer Unknown Channel. `cmd/s3-orphans`
reports sticky files that no sticky refers to.

## Keeping it this way

Any change that reads another user's message content, embeds or attachments,
whether from gateway events or REST, would put Message Content back on
stickybot. Use the context-menu resolved target instead. If that is truly
impossible, get explicit maintainer approval first (see `CLAUDE.md`).
