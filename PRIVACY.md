# Privacy Policy

This policy covers the Discord applications operated from this repository for the
Espresso Aficionados Discord server: **sprobot**, **stickybot**, and **threadbot**,
plus the **sprobot-web** profile page server.

## Data we collect and why

### sprobot (profiles and moderation)

- **User profiles** — text and an optional image that you explicitly submit via the
  `/edit<template>` slash commands. Stored in a private S3 bucket, keyed by server and
  user ID, and rendered as a web page by sprobot-web so profiles can be linked from
  Discord. You can view your data at any time with `/get<template>` and permanently
  delete it with `/delete<template>`.
- **Message counts** — a per-user count of messages posted per server, used for the
  top-poster role feature. Counts only; no message text is stored.
- **Moderation event log** — message edits and deletions, member joins and leaves, and
  moderation actions (bans, kicks, timeouts, warnings) are posted to a staff-only
  channel **on Discord**. To show the content of an edited or deleted message, the bot
  keeps a bounded, in-memory cache of recent messages. This cache is never written to
  storage outside Discord and is discarded whenever the bot restarts.
- **Temporary roles** — the user ID, role ID, and expiry time of staff-assigned
  temporary roles.
- **Server configuration** — channel/role IDs and staff-authored text (welcome
  messages, shortcut responses).

### stickybot

- **Sticky messages** — when a moderator designates a message as a sticky, its text and
  attachments are copied to S3 so the sticky can be reposted. Only staff-designated
  messages are stored, and they are removed when the sticky is removed.

### threadbot

- **Thread reminder configuration** and per-thread **member counts** (numbers only).

## What we do not do

- We do not store regular message content outside Discord.
- We do not sell or share any data with third parties.
- We do not use any data to train machine-learning or AI models.
- We do not track presence/online status.

## Storage and security

Persistent data lives in a private S3 bucket accessible only to the bot
infrastructure. Profile pages served by sprobot-web are public URLs by design — do not
put anything in a profile you do not want visible outside Discord.

## Data removal

Delete your profile at any time with `/delete<template>`. For removal of any other
data, contact the server staff or open an issue at
<https://github.com/Espresso-Aficionados/sprobot/issues>.

## Changes

Changes to this policy are tracked in this repository's git history.

## Terms

Use of the services is also governed by the [Terms of Service](TERMS.md).
