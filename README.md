# Herdr Whistle

Herdr plugin for remote agent management. List agents, read their output, send text to unblock them, close panes, and start new agents -- all from Telegram.

## Prerequisites

- Go 1.26+
- [herdr](https://herdr.dev) 0.7.5+
- A Telegram bot token (from [@BotFather](https://t.me/BotFather))
- Your Telegram user ID

## Installation

### 1. Build the binary

```sh
git clone https://github.com/yzaimoglu/herdr-whistle
cd herdr-whistle
go build -o herdr-whistle .
```

### 2. Link the plugin in herdr

```sh
herdr plugin link /path/to/herdr-whistle
```

### 3. Create configuration

herdr sets `HERDR_PLUGIN_CONFIG_DIR` automatically. Find your plugin config directory:

```sh
herdr plugin config-dir herdr-whistle
```

Create `config.toml` in that directory:

```toml
token = "1234567890:ABCdefGHIjklmNOPqrStuVWXyz"
owner_id = 00000000
chat_id = 00000000
```

Protect the bot token before starting the plugin:

```sh
chmod 600 "$(herdr plugin config-dir herdr-whistle)/config.toml"
```

| Field      | Description                                                          |
| ---------- | -------------------------------------------------------------------- |
| `token`    | Bot token from [@BotFather](https://t.me/BotFather)                  |
| `owner_id` | Your Telegram user ID (integer) -- only this user can issue commands |
| `chat_id`  | Your private chat ID with the bot -- must equal `owner_id`             |

## Usage

### Start the plugin

```sh
herdr plugin action invoke herdr-whistle.start
```

The action starts a detached daemon that survives temporary Herdr restarts. Workspace events ensure it is started again if it exits.

### Stop the plugin

Use the plugin's stop action:

```sh
herdr plugin action invoke herdr-whistle.stop
```

### View logs

The detached daemon writes its own restricted log file:

```sh
tail -f ~/.config/herdr-whistle/bot.log
```

### Update the plugin

After pulling changes or rebuilding:

```sh
go build -o herdr-whistle .
CONFIG_DIR="$(herdr plugin config-dir herdr-whistle)"
# Version 0.2.0 requires chat_id in config.toml; set it to your owner_id.
chmod 600 "$CONFIG_DIR/config.toml"
herdr plugin action invoke herdr-whistle.stop
herdr plugin action invoke herdr-whistle.start
```

## Commands

All commands are restricted to the configured `owner_id` in the configured private `chat_id`. Other users and chats are ignored.

For normal use, send `/start` and use the dashboard:

1. Select an agent to open its detail card.
2. Use **Send prompt** and reply to the Force Reply message.
3. Use **Read output** for the latest terminal output.
4. Use **Interrupt** or **Close pane** with the confirmation buttons.
5. Blocked notifications always offer **Send response**, **Read output**, and **Open agent**. Recognized selection menus also show verified one-use buttons labeled with each option.
6. Filter or page through larger agent lists, switch to compact mode, review recent activity, and configure lifecycle notifications from the dashboard.
7. Use **Start agent** or bare `/startagent` for a button-driven pane, kind, and name wizard.

Direct option buttons recognize OpenCode question and permission forms, Claude Code permission/navigation forms, and Codex approval or `[y/n]` prompts. Claude cursor movement is sent and visibly verified one key at a time before Enter is submitted. OpenCode permissions expose deterministic **Allow once** and **Reject** actions. Unrecognized or free-answer forms stay on the safe **Send response** fallback.

| Command        | Arguments            | Description                                    |
| -------------- | -------------------- | ---------------------------------------------- |
| `/start`       | --                   | Open the agent dashboard                       |
| `/help`        | --                   | Available commands                             |
| `/agents`      | --                   | List active agents with action buttons         |
| `/activity`    | --                   | Show recent lifecycle and control activity     |
| `/notifications` | --                 | Configure blocked and completed alerts          |
| `/status`      | `<target>`           | Show agent status and explanation              |
| `/read`        | `<target>` `[N]`     | Read up to 200 lines of agent output           |
| `/send`        | `<target>` `<text>`  | Send text to an agent (unblocks it)            |
| `/close`       | `<target>`           | Close an agent's terminal pane                 |
| `/startagent`  | `[<name> <kind> <pane> [-- args]]` | Open the wizard or start an agent directly |

### Examples

```
/status my-agent
/read my-agent 50
/send my-agent continue solving the problem
/close my-agent
/startagent code-helper opencode w1:p2
/startagent reviewer claude w1:p3 -- --model sonnet
```

### Command details

**`/send`** is the primary way to unblock agents. It uses Herdr's guarded `agent prompt` API, which refuses to submit to a pane that no longer contains the intended agent.

**`/close`** asks for confirmation, binds it to the current terminal identity, and then closes the pane. Stale confirmations are rejected.

**`/startagent`** without arguments opens the guided pane, agent-kind, and name picker. The direct form follows Herdr 0.7.5's contract; arguments after `--` are passed to the agent executable.

**`/read`** fetches output via `herdr agent read --source recent-unwrapped` to show the agent's most recent lines.

## Architecture

```
Telegram  <--long-poll-->  herdr-whistle  --herdr CLI-->  herdr daemon
   user                    (herdr plugin)
```

- The plugin connects to Telegram via long-polling (no webhooks)
- All herdr interactions use the `herdr` CLI via `os/exec` with 30-second timeouts
- Authentication is enforced server-side: both `owner_id` and private `chat_id` must match
- Pane mutations are serialized per pane
- Dashboard actions use short-lived server-side tokens rather than raw pane data
- Prompt replies and confirmations are one-use and revalidate terminal, session, and state generation
- Choice buttons expire after ten minutes and validate the visible blocked prompt before sending keys; cursor menus move one key at a time and verify each resulting cursor position before Enter
- The binary is a single self-contained executable, no runtime dependencies

The daemon controls the Herdr session that launched it. Running multiple named
Herdr servers with one bot token is not currently supported.

## Files

| File                | Purpose                                                            |
| ------------------- | ------------------------------------------------------------------ |
| `herdr-plugin.toml` | Plugin manifest (id, version, start action)                        |
| `main.go`           | Entry point, config loading, signal handling                       |
| `bot.go`            | Telegram bot setup, handler registration, message senders          |
| `handlers.go`       | Command handlers, owner/chat auth, HTML escaping                    |
| `herdrcli.go`       | Herdr CLI wrappers, including guarded agent prompt/send-keys calls  |
| `config.go`         | TOML config loading                                                |
| `watcher.go`        | Agent status watcher -- notifies on blocked transitions            |
| `go.mod` / `go.sum` | Go module dependencies                                             |

## Security

- Only the configured `owner_id` in the configured private `chat_id` can interact with the bot
- No command parsing or execution beyond `herdr` CLI calls
- herdr CLI is invoked via the `HERDR_BIN_PATH` env var (falls back to `herdr` PATH lookup)
- All bot messages use plain text or properly escaped HTML
- Configuration must be a regular file with mode `0600`
