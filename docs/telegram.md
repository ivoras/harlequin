# Telegram

Harlequin can be reached through a Telegram bot: a user links their Telegram
chat to their Harlequin account once, then talks to the agent by messaging the
bot. The same bot delivers notifications (cron results, watches) to users who
choose the `telegram` channel.

## What the server needs

- **Outbound HTTPS to `api.telegram.org`.** Nothing else: the server
  long-polls Telegram for messages, so there is no inbound port, public URL,
  webhook or certificate to configure.
- **One bot per deployment.** Two servers must never share a bot token —
  Telegram hands each message to only one poller, so they would steal each
  other's messages (and both log `409 Conflict`). This includes a development
  server started with a copy of the production `.env`.

## Setup

1. **Create the bot.** In Telegram, open a chat with
   [@BotFather](https://t.me/BotFather) and send `/newbot`. Choose a display
   name (e.g. "Acme Harlequin") and a username ending in `bot` (e.g.
   `acme_harlequin_bot`). BotFather replies with a token like
   `123456789:AAH...`.
2. **Keep it out of groups** (recommended). Send BotFather `/setjoingroups`,
   pick the bot, choose *Disable*. Harlequin ignores group messages anyway.
3. **Give the token to the server.** Add it to `.env`:

   ```
   TELEGRAM_BOT_TOKEN=123456789:AAH...
   ```

   and make sure `server.yaml` names that variable:

   ```yaml
   telegram:
     bot_token_env: TELEGRAM_BOT_TOKEN
   ```

4. **Restart the server.** The log shows `telegram: chatting as @<username>`
   when the token works. The server sets the bot's command menu (`/new`,
   `/stop`) itself.

Optional, in BotFather: `/setdescription`, `/setabouttext` and `/setuserpic`
change what users see when they open the bot.

The token is a secret: anyone holding it can read what users send the bot.
If it leaks, send BotFather `/revoke`, put the new token in `.env` and restart.

## Connecting a user

Each user connects their own Telegram chat:

1. In the web app open **Config** and choose **Connect Telegram** (desktop:
   Settings → Config), or type `/telegram` in the TUI. Either shows a one-time
   link.
2. Open the link on a device with Telegram and press **Start**. The bot
   answers "Connected to <email>".

The link works once and expires after 10 minutes (and is void if the server
restarts in between). A user has one connected chat: connecting another
replaces it. Connecting also registers the chat for `telegram` notifications.

To disconnect, use **Disconnect** in the same place or `/telegram disconnect`.
Deleting the account disconnects it too.

The clients use `GET|POST|DELETE /api/v1/telegram/link` (status, new link,
disconnect).

## Using the bot

Send the bot a message; it answers when the agent's turn is finished ("typing…"
shows while it works). The conversation is a normal personal session — it
appears in the session list of the web app and the TUI (interface `Telegram`),
with the same memory, documents, skills and sticky hat.

| Command | Effect |
|---------|--------|
| `/new` | Start a fresh session; later messages go there. |
| `/stop` | Interrupt the reply in progress. |

Current limits:

- Text only, both ways. Photos and files sent to the bot are not read, and
  files the agent creates are not sent to the chat (open them in the web app).
- Replies are plain text, so Markdown formatting shows as typed.
- Private chats only; no project sessions.

## Troubleshooting

| Symptom | Cause |
|---------|-------|
| Log: `getUpdates: 409 Conflict…` | Another process is polling the same token (a second server, or a webhook set on the bot). |
| Log: `getMe: 401 Unauthorized` | Wrong or revoked token. |
| No Connect Telegram button; `/telegram` says "not set up" | No bot token configured on this server. |
| Bot says "This chat is not connected…" | The chat is not linked (or was replaced by another chat); create a new link. |
| A notification never arrives | The user has not pressed Start in the bot: a bot cannot message someone first. |
