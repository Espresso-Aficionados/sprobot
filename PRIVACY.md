# Privacy Policy

This policy covers the Discord applications operated from this repository for the
Espresso Aficionados Discord server: **sprobot**, **stickybot**, and **threadbot**,
plus the **sprobot-web** server, which renders profile pages and files saved by the bots.

## Data we collect and why

### sprobot (profiles and moderation)

- **User profiles** — text and an optional image that you explicitly submit via the
  `/edit<template>` slash commands. Your server display name is saved alongside your
  profile (shown in link previews of your profile page). Stored in a private S3 bucket,
  keyed by server and user ID, and rendered as a web page by sprobot-web so profiles
  can be linked from Discord. You can view your data at any time with `/get<template>` and permanently
  delete it, image included, with `/delete<template>`.
- **Top-poster counts** — per-user daily message counts for the last 7 days, with each
  counted user's last-known username, for the staff `/topposters` command. Days older
  than 7 days, and the usernames of users with no remaining counts, are dropped.
  Counts only; no message text is stored.
- **Poster-role counts** — a per-user count of messages in the channels that earn the
  poster role. A user's count is removed when sprobot grants them the role, or at
  their next message once they have it.
- **Moderation event log** — message edits and deletions, member joins and leaves, and
  moderation actions (bans, kicks, timeouts, warnings) are posted to a staff-only
  channel **on Discord**. To show the content of an edited or deleted message, the bot
  keeps a size-bounded cache of recent messages on its own infrastructure (in memory,
  with an encrypted-at-rest on-disk copy so the log survives restarts). Cached
  messages are deleted automatically after at most 30 days. The cache is used for the
  moderation log and, in limited form, for the development and troubleshooting
  described below.
- **Server member cache** — Discord's record of server members, such as user ID,
  username, display name, avatar, server nickname, roles, and join date, so the
  moderation log can show nickname changes and who left, and for staff commands such
  as `/topposters`. Only members of the servers sprobot is set up to serve are cached.
  It has no time limit: a member's record is removed when the bot sees them leave the
  server, so a member who leaves while the bot or the server is offline stays cached.
  Alongside it the bot caches other server details Discord sends it, such as channels,
  roles, and who is in a voice channel.
- **Starboard** — when a message gets enough reactions, sprobot reposts its text, first
  image, and author to the starboard channel on Discord. Messages are not reposted from
  private threads, from channels or categories on the staff's starboard filter list,
  or from age-restricted channels when the starboard channel isn't age-restricted. The
  repost is removed when the bot sees the original message deleted, so a message
  deleted while the bot is offline keeps its repost. The bot stores the message,
  channel, and author IDs, the channel name, and the reaction count. Reactions are
  counted for 90 days after a message is sent: older messages aren't reposted, and a
  repost's count stops updating. Entries for messages that were never reposted are
  dropped after 30 days; a reposted message's entry is kept until the bot sees the
  original deleted, so the repost can be removed with it.
- **Temporary roles** — the user ID, role ID, and expiry time of staff-assigned
  temporary roles.
- **Server configuration** — channel, role, and user IDs set by staff (for example,
  users excluded from top-poster counts) and staff-authored text (welcome messages,
  shortcut responses and their images).

### stickybot

- **Sticky messages** — when a moderator designates a message as a sticky, its text,
  embeds, and attachments are copied to S3 so the sticky can be reposted. Only
  staff-designated messages are stored, and they are removed, saved attachments
  included, when the sticky is removed.

### threadbot

- **Thread reminder configuration**, the IDs of threads staff have hidden from
  reminders, and per-thread **member counts** (numbers only; a count not refreshed for
  7 days is dropped).

## Moderator-private data

Some data is visible only to moderators and is kept for moderation purposes. When a
moderator saves a message to the **mod log**, its text, author, and a moderator's note
are posted in a moderator-only thread on Discord, and its attachments are copied to
private storage so the record survives if the original is deleted, reachable only
through unguessable links posted in that thread. Saved attachments are not deleted
automatically. Moderation actions involving a member (such as bans, warnings, and
temporary roles), and tickets they open, are recorded in the same threads. These
records are not removed by `/delete<template>`; contact the server staff about them.
To find each member's thread, sprobot keeps an in-memory map of user IDs to thread
IDs, plus the names of older threads whose name has no user ID; it is rebuilt from
Discord after a restart and never written to disk.

## Development and troubleshooting

For messages sent on or after 2026-10-15, the bot maintainers may evaluate the
moderation cache to find bugs or to test new or changed bot features against
previously sent messages (for example, checking that a new feature that handles
links in messages works correctly on real links). When we do this:

- We extract only what the evaluation needs (for example, the URLs in a message), not
  message text, usernames, or user IDs.
- Extracted data may be processed with AI development tools (currently Anthropic's
  Claude) acting on our behalf. It is not used to train AI models.
- Extracted data is deleted within 30 days, the same limit as the cache itself, and is
  never published.

## What we do not do

- Outside the 30-day moderation cache, we keep message content only where described
  above: the moderation log and mod log on Discord, sticky messages, and starboard
  reposts. We do not share it with anyone except as described under "Development and
  troubleshooting".
- We do not sell data, and we do not share it with third parties except the service
  providers named above that process it on our behalf.
- We do not use any data to train machine-learning or AI models.
- We do not track presence/online status.

## Storage and security

Persistent data lives in a private S3 bucket, encrypted at rest and accessible
only to the bot infrastructure. sprobot's caches are stored on the bot host's
encrypted disk. Time-bounded records (such as temporary-role assignments) are
removed automatically when they expire. A temporary-role record is kept past its
expiry only until the bot has removed the role, or the member or role no longer
exists. Profile pages served by sprobot-web are
public URLs by design — do not put anything in a profile you do not want visible
outside Discord. Links to saved files are unguessable, but work for anyone who has them.
The bots and sprobot-web write operational logs for troubleshooting; these include
user IDs and, for sprobot-web, visitors' IP addresses and user agents.

If sprobot finds stored data it cannot read back (for example top-poster counts or
temporary-role assignments saved in a format it no longer understands), it keeps a
copy in the same private bucket for up to 30 days so the maintainers can recover it,
then deletes it automatically. Records in that copy are kept for those 30 days even if
they would otherwise have expired sooner.

## Data removal

Delete your profile, including its image, at any time with `/delete<template>`. For
removal of any other data, contact the server staff or open an issue at
<https://github.com/Espresso-Aficionados/sprobot/issues>.

## Changes

Changes to this policy are tracked in this repository's git history.

## Terms

Use of the services is also governed by the [Terms of Service](TERMS.md).
