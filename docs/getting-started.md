# Getting started

Install llm-brain on an always-on Mac (the hub and fleet) and, optionally, on laptops that only ship their sessions.

## What you need

- macOS on Apple silicon. The hub builds for `darwin/arm64` only.
- [Homebrew](https://brew.sh). `bootstrap.sh` stops if it is missing.
- [Tailscale](https://tailscale.com), so other Macs and your phone can reach the hub.
- A GitHub account with `gh` logged in, and at least one of Claude Code, Codex or Gemini CLI logged in.
- `tmux` if you want hub terminals (`brew install tmux`). Bootstrap does not install it.
- Python 3 for `bin/chat-watch`, and Python 3.13 or newer for `bin/review-rules` (it uses `PurePosixPath.full_match`). Bootstrap installs neither.

## Install the hub Mac

```sh
git clone https://github.com/anchoo2kewl/llm-brain ~/projects/llm-brain
mkdir -p ~/projects/llm-brain-overlay
cp -R ~/projects/llm-brain/examples/overlay/. ~/projects/llm-brain-overlay/
$EDITOR ~/projects/llm-brain-overlay/config.yaml
~/projects/llm-brain/bootstrap.sh
```

Fill in the overlay first: who you are, who hands you work, which repos. See [Overlay](overlay.md).

Then open `http://<this-mac>:8787` (or `http://127.0.0.1:8787` on the Mac itself).

## What bootstrap does

`bootstrap.sh` is idempotent. Run it again after a pull or a config change.

1. Installs tools with Homebrew: `gh jq yq coreutils fswatch codex node rsync go`.
2. Installs open-code-review (`npm install -g @alibaba-group/open-code-review`) if `ocr` is missing.
3. Installs Claude Code with its own installer if `claude` is missing.
4. If your config has `config_sync.repo`, clones it to `config_sync.dir` and runs its `restore` script.
5. Clones every repo in `<overlay>/repos.txt` into `work_root`.
6. Builds the hub: `make -C hub build` writes `bin/hub`.
7. Removes the old `com.llm-brain.dashboard` launchd job if present.
8. Loads the `com.llm-brain.fleet` and `com.llm-brain.hub` launchd jobs.
9. Prints which logins are missing (`gh`, `claude`, `codex`) and runs `fleet providers`.

## Flags

| Flag | Effect |
| --- | --- |
| `--no-clone` | Skip cloning the repos in `<overlay>/repos.txt`. |
| `--no-fleet` | Install and build, but don't load the fleet loop or the hub. |
| `--shipper` | Laptop mode: build the hub binary and load only `com.llm-brain.hub-ship`. Nothing else is installed. |
| `--hub=URL` | The hub a shipper sends to. Default: `hub_url` from the config. |

## Launchd jobs

The plists in `launchd/` are templates. Bootstrap fills in paths, the hub URL, the first `pr_owners` org, the first intake repo and your `people`, writes them to `~/Library/LaunchAgents/`, and loads them.

| Job | Runs | When | Log |
| --- | --- | --- | --- |
| `com.llm-brain.fleet` | `bin/fleet tick` | Every 300 s and at load | `~/.local/state/llm-brain/logs/fleet.log` |
| `com.llm-brain.hub` | `bin/hub serve --port 8787 --org … --issue-repo … --people …` | Always on (restarted after 10 s) | `~/.local/state/llm-brain/logs/hub.log` |
| `com.llm-brain.hub-ship` | `bin/hub ship --to <hub_url>` | Always on (restarted after 30 s) | `~/.local/state/llm-brain/logs/hub-ship.log` |

To restart one by hand:

```sh
launchctl kickstart -k gui/$(id -u)/com.llm-brain.hub
```

## Logins

Bootstrap tells you which are missing. The commands it suggests:

```sh
gh auth login -h github.com -p ssh -w
claude auth login
codex login --device-auth
```

## A laptop that only ships

```sh
git clone https://github.com/anchoo2kewl/llm-brain ~/projects/llm-brain
~/projects/llm-brain/bootstrap.sh --shipper --hub=http://<hub-mac-tailscale-ip>:8787
```

The shipper sends this Mac's Claude and Codex transcripts and fleet logs to the hub every 10 s. It also runs the session agent, so the hub can open terminals and tasks on the laptop. It polls the hub for commands every 3 s; nothing listens on the laptop. Run it with `--agent=false` to make it ship-only.

## Reach the hub from a phone

Browsers need HTTPS for the installable app and push notifications. Serve the hub through Tailscale once on the hub Mac:

```sh
tailscale serve --bg 8787
```

Open `https://<mac>.<tailnet>.ts.net`, add it to the home screen, and turn notifications on in Settings. See [Hub](hub.md#installable-app-and-push).

## Next

- [Configuration](configuration.md) for every setting.
- [Fleet](fleet.md) to start taking issues: run `bin/fleet labels` once.
- [Team chat](chat.md) to connect Slack.
