# Privacy Policy

This policy covers the Discord applications operated from this repository for the
Espresso Aficionados Discord server: **sprobot**, **stickybot**, and **threadbot**,
plus **sprobot-web**, which serves profile pages and files the bots have saved.

## Data we collect and why

### sprobot (profiles and moderation)

- **User profiles** — text and an optional image that you explicitly submit via the
  `/edit<template>` slash commands. Your server display name is saved alongside your
  profile (shown in link previews of your profile page). Stored in a private S3 bucket,
  keyed by server and user ID, and rendered as a web page by sprobot-web so profiles
  can be linked from Discord. You can view your data at any time with `/get<template>` and permanently
  delete it with `/delete<template>`.
- **Message counts** — a per-user count of messages posted per server, used for the
  top-poster role feature. Counts only; no message text is stored.
- **Moderation event log** — message edits and deletions, member joins and leaves, and
  moderation actions (bans, kicks, timeouts, warnings) are posted to a staff-only
  channel **on Discord**. To show the content of an edited or deleted message, the bot
  keeps a size-bounded cache of recent messages on its own infrastructure (in memory,
  with an encrypted-at-rest on-disk copy so the log survives restarts). Cached
  messages are deleted automatically after at most 30 days. The cache is used for the
  moderation log and, in limited form, for the development and troubleshooting
  described below.
- **Mod log** — some data is visible only to moderators and kept for moderation
  purposes. Moderators can save a message to a staff-only mod log: its text, author,
  and a moderator's note are posted to a staff-only channel **on Discord**, and its
  attachments are copied to S3 so the record survives if the original is deleted.
  Saved attachments are not deleted automatically. sprobot-web serves them at
  unguessable links that appear only in the mod log; anyone holding a link can open
  the file.
- **Temporary roles** — the user ID, role ID, and expiry time of staff-assigned
  temporary roles.
- **Server configuration** — channel/role IDs and staff-authored text (welcome
  messages, shortcut responses and their images).

### stickybot

- **Sticky messages** — when a moderator designates a message as a sticky, its text and
  attachments are copied to S3 so the sticky can be reposted. Only staff-designated
  messages are stored, and they are removed when the sticky is removed.

### threadbot

- **Thread reminder configuration** and per-thread **member counts** (numbers only).

## Development and troubleshooting

For messages sent on or after 2026-10-15, the bot maintainers may evaluate the
moderation cache to find bugs or to test new or changed bot features against
previously sent messages (for example, checking that the link cleaner handles real
links correctly). When we do this:

- We extract only what the evaluation needs (for example, the URLs in a message), not
  message text, usernames, or user IDs.
- Extracted data may be processed with AI development tools (currently Anthropic's
  Claude) acting on our behalf. It is not used to train AI models.
- Extracted data is deleted within 30 days, the same limit as the cache itself, and is
  never published.

## What we do not do

- We do not retain message content beyond the 30-day moderation cache described
  above, except messages staff deliberately save as stickies or to the mod log, and
  we do not share it with anyone except as described under "Development and
  troubleshooting".
- We do not sell data, and we do not share it with third parties except the service
  providers named above that process it on our behalf.
- We do not use any data to train machine-learning or AI models.
- We do not track presence/online status.

## Storage and security

Persistent data lives in a private S3 bucket, encrypted at rest and accessible
only to the bot infrastructure. Time-bounded records (such as temporary-role
assignments) are removed automatically when they expire. A temporary-role record
is kept past its expiry only until the bot has removed the role, or the member or
role no longer exists. Profile pages served by sprobot-web are public URLs by design — do not
put anything in a profile you do not want visible outside Discord.

If sprobot finds stored data it cannot read back (for example top-poster counts or
temporary-role assignments saved in a format it no longer understands), it keeps a
copy in the same private bucket for up to 30 days so the maintainers can recover it,
then deletes it automatically. Records in that copy are kept for those 30 days even if
they would otherwise have expired sooner.

## Data removal

Delete your profile at any time with `/delete<template>`. For removal of any other
data, contact the server staff or open an issue at
<https://github.com/Espresso-Aficionados/sprobot/issues>.

## Changes

Changes to this policy are tracked in this repository's git history.

## Terms

Use of the services is also governed by the [Terms of Service](TERMS.md).
