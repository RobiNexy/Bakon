# Bakon

> Effortlessly keep version history while editing critical files.

[English](README.en.md) | 中文

[![CI](https://github.com/RobiNexy/Bakon/actions/workflows/ci.yml/badge.svg)](https://github.com/RobiNexy/Bakon/actions/workflows/ci.yml)

Bakon is a single-machine command-line tool: when you edit critical files like `nginx.conf`, `hosts`, or crontab with your familiar editor, Bakon quietly stores every change into a git repository, supports viewing diffs, exporting historical versions, one-click rollback, and can automatically run a hook after a file changes (e.g. `systemctl reload nginx`).

- **Single machine, single user, no collaboration** — no remote, no branches, no conflicts
- **Depends only on `git`** as the version storage engine
- **Doesn't touch your workflow** — the editor edits the target file in place; Bakon only keeps copies inside its repository

## Features

- `edit`: opens the editor in the foreground; silent exit if content is unchanged, automatic new version if changed
- Per-file **integer version numbers** (ver), append-only, rollback never overwrites history
- Per-file **retention limit** (oldest versions pruned automatically) and **change hooks**
- Concurrency-safe: a file lock serializes all write operations
- Ships as static binaries covering Linux / macOS / Windows / Android Termux

## Installation

**Option 1: download a prebuilt archive** (recommended)

Download the archive for your platform from [Releases](../../releases). sha256 checksums are in `checksums.txt`.

| Archive | Platform |
|---|---|
| `bakon_<ver>_linux-amd64.tar.gz` | Linux x86-64 |
| `bakon_<ver>_linux-arm64.tar.gz` | Linux arm64 / **Android Termux (aarch64)** |
| `bakon_<ver>_linux-armv7.tar.gz` | Linux armv7 / Android Termux (32-bit) |
| `bakon_<ver>_darwin-amd64.tar.gz` | macOS (Intel) |
| `bakon_<ver>_darwin-arm64.tar.gz` | macOS (Apple Silicon) |
| `bakon_<ver>_windows-amd64.zip` | Windows x86-64 |
| `bakon_<ver>_windows-arm64.zip` | Windows arm64 |

```sh
# Linux / macOS (replace BAKON_VER with the latest tag):
BAKON_VER=v0.1.0
curl -fLo /tmp/bakon.tar.gz \
  "https://github.com/RobiNexy/Bakon/releases/download/${BAKON_VER}/bakon_${BAKON_VER}_linux-amd64.tar.gz"
tar -xzf /tmp/bakon.tar.gz -C /tmp
mv "/tmp/bakon_${BAKON_VER}_linux-amd64/bakon" /usr/local/bin/

# Windows (PowerShell):
Invoke-WebRequest `
  "https://github.com/RobiNexy/Bakon/releases/download/v0.1.0/bakon_v0.1.0_windows-amd64.zip" `
  -OutFile bakon.zip
Expand-Archive bakon.zip
```

**Option 2: go install**

```sh
go install github.com/RobiNexy/Bakon@latest
```

**Option 3: build from source**

```sh
git clone https://github.com/RobiNexy/Bakon && cd Bakon
scripts/build.sh          # per-platform archives in dist/
go build .                # or build for this machine only
```

**Android Termux** (requires installing git at runtime):

```sh
pkg install git
BAKON_VER=v0.1.0   # replace with the latest tag
curl -fLo /tmp/bakon.tar.gz \
  "https://github.com/RobiNexy/Bakon/releases/download/${BAKON_VER}/bakon_${BAKON_VER}_linux-arm64.tar.gz"
tar -xzf /tmp/bakon.tar.gz -C /tmp
mv "/tmp/bakon_${BAKON_VER}_linux-arm64/bakon" $PREFIX/bin/
```

> Runtime dependencies: `git` (Bakon calls the system git as its storage engine) and a shell (the editor and hooks are launched through a shell: `sh` on unix, `cmd /C` on Windows — hook scripts must be executable on the target platform on their own).

## Quick start

```sh
# 1. Edit any file, Bakon takes over version history automatically
bakon edit /etc/nginx/nginx.conf

# 2. View history
bakon log /etc/nginx/nginx.conf
#   #  ver   time                  size
#     3     2025-01-15 10:23:41   1.2K
#     2     2025-01-10 09:01:12   1.1K
#     1     2025-01-03 18:47:00   1.0K

# 3. See the diff (defaults to the two most recent versions)
bakon diff /etc/nginx/nginx.conf

# 4. Export a version's content
bakon show /etc/nginx/nginx.conf 2

# 5. Roll back to version 2 (history untouched, creates a new version)
bakon revert /etc/nginx/nginx.conf 2

# 6. Auto-reload after the file changes
bakon hook set /etc/nginx/nginx.conf "systemctl reload nginx"
```

## Commands

| Command | Purpose |
|---|---|
| `bakon edit <file>` | Open the editor; silent exit if unchanged, new version if changed |
| `bakon log <file>` | List historical versions (number, time, size) |
| `bakon diff <file> [v1] [v2]` | Compare versions; defaults to the two most recent; one argument compares the previous version |
| `bakon show <file> <v>` | Print the full content of a version |
| `bakon revert <file> <v>` | Restore to a version, committed as a new version |
| `bakon ls` | List all managed files |
| `bakon mv <old> <new>` | Update the path mapping, keeping history |
| `bakon prune [<file>]` | Trim history to the retention limit; defaults to all files |
| `bakon hook set/unset/show` | Manage per-file change hooks |
| `bakon config show` | Show effective configuration and file locations |
| `bakon config init` | Write the commented default config file (never overwrites) |
| `bakon version` | Print version information |

## Configuration

The config file lives at `~/.bakon/config.toml` (use `bakon config show` to see the actual path and effective values; the global `--config` flag selects a different location). **When the file is absent, every command still works with defaults** — no pre-initialization needed. To get an editable, commented config, run `bakon config init` (never overwrites an existing one).

```toml
editor = "vim"              # editor; priority: config > $EDITOR > vi
store  = "~/.bakon/repo"    # repository location

[retention]
max_versions = 100          # per-file retention limit; 0 = unlimited
```

Per-file settings (`max_versions`, `hook`) live in the repository's `index.json`, managed via the `bakon hook` commands, not in the global config.

## Hooks and permissions (sudo)

A hook is **any shell command**: writing `sudo systemctl reload nginx` runs it as-is; Bakon does not intercept, interpret, or escalate on your behalf — what matters is the identity it runs as:

| Running as | Recommendation |
|---|---|
| root (the typical way to edit `/etc` files) | **Don't write sudo** in hooks — plain `systemctl reload nginx`; Bakon is already root, and wrapping sudo fails on some sudoers setups (e.g. `requiretty`) |
| non-root user, hook needs root | When `sudo` cannot read a password (no TTY, and the hook's stdin is `/dev/null`) it **fails immediately instead of hanging**; the error output is visible and Bakon exits with code 2 while the version is saved. The right fix: a precise sudoers whitelist |

Example of a precise sudoers whitelist (least privilege, one command only):

```text
samphi ALL=(root) NOPASSWD: /usr/bin/systemctl reload nginx
```

Alternatives: polkit (`pkexec`), or a systemd path unit watching the file instead of a hook. Bakon does not cache credentials and does not escalate — part of the "no permission hardening" boundary.

> sudo's exact behavior without a TTY varies by version and sudoers configuration [inferred from common patterns]; verify with `sudo -l -n` on the target environment.

## Key points

- **ver numbers are never recycled**: after pruning, `log` starts from the oldest retained version; assigned numbers are never reused.
- **revert creates a new version**: rollback is an appended commit, history is never rewritten; if the target content matches the current version, no new version is created.
- **Pruned versions are unrecoverable**: `show`/`revert` of a pruned number reports `has been pruned` (distinct from a never-existing `no version N`).
- **No data loss on crash**: if the process dies after the editor exits but before the commit, the target file is changed while the repository has no version — the next `bakon edit` commits the current content as a new version. No recovery mechanism needed.
- **Concurrency model**: write operations (edit/revert/prune/mv/hook set) are serialized by the repository file lock; read-only commands (ls/log/diff/show) **take no lock** — atomic index writes + immutable git objects guarantee no torn state, at the cost of a snapshot that may lag one commit behind, and they are never blocked by someone else's pending editor session.
- **Hooks fire when a new version is created** (after an edit or revert commit), run inside the file lock; a hook failure does not affect the completed version commit, and the process exits with code 2.
- **Exit codes**: `0` success; `1` operation failure; `2` version saved but hook failed.

The design document is at [docs/design.md](docs/design.md).

## License

[MIT](LICENSE)
