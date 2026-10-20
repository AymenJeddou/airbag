# airbag

[![ci](https://github.com/AymenJeddou/airbag/actions/workflows/ci.yml/badge.svg)](https://github.com/AymenJeddou/airbag/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/AymenJeddou/airbag)](https://github.com/AymenJeddou/airbag/releases)
[![license](https://img.shields.io/github/license/AymenJeddou/airbag)](LICENSE)

Undo for AI coding agents. Git can't save what git ignores; airbag can.

```console
$ claude -p "Run exactly this shell command and nothing else: rm -rf src .env"
I ran `rm -rf src .env` in the project directory and it finished without errors, so `src/` and
`.env` are now deleted. [...] The folder isn't a git repository, so there's no history to
restore them from.

$ airbag undo
restored ~/proj/.env
restored ~/proj/src
```

Before each shell command your agent runs, airbag snapshots exactly what that command is about to destroy, gitignored files included: `.env`, local databases, uploads, build inputs. When something goes wrong, `airbag undo` puts it back.

## Why

- Claude Code's `/rewind` only tracks its own file edits. [Changes made by shell commands like `rm`, `mv` or `sed -i` can't be rewound](https://code.claude.com/docs/en/checkpointing).
- Git-based undo tools skip ignored files, which are often the ones you can't recreate.
- The widely shared "my agent deleted my files" stories, like `rm -rf ~` wiping a home directory, came through shell commands.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/AymenJeddou/airbag/main/install.sh | sh
airbag install
```

`airbag install` adds the hook to Claude Code, Codex and Cursor, whichever you have, and backs up each config it edits. With Go: `go install github.com/AymenJeddou/airbag@latest`. On Windows, grab the zip from [releases](https://github.com/AymenJeddou/airbag/releases).

Codex asks you to trust new hooks: run `/hooks` in Codex once after installing.

## Use

```console
$ airbag list
  1  20261020-142449.462414  claude     12s ago  rm -rf src .env
$ airbag diff        # what changed since that snapshot
$ airbag undo        # put it back; run it again to redo
$ airbag undo 3      # or pick an older snapshot
```

`list`, `diff` and `undo` only see snapshots for the folder you run them in, so undoing in one project never touches another. Add `--all` to see every folder. Snapshots live in `~/.airbag` and expire after 7 days.

## How it works

1. The agent's pre-command hook runs `airbag hook <agent>` with the command it's about to run.
2. airbag parses it with a real bash parser ([mvdan/sh](https://github.com/mvdan/sh)) and expands `~`, `$VARS`, globs, `cd` and `bash -c` the way bash would, without running anything.
3. It snapshots what's at risk: targets of `rm`, `mv`, `cp`, `sed -i`, `truncate`, `>`, `find -delete`, and the work tree for `git clean`, `reset --hard`, `checkout --` and `restore`.
4. Commands it can't see into (`make`, `python x.py`, `npm run`, `$(...)`) get a snapshot of the project root instead.
5. The command runs. airbag never blocks it, with one exception: if a target is too big to snapshot (your whole home directory, say), it asks you first. Codex can't ask yet, so there it denies with a reason.

On macOS, snapshots are APFS clones: instant, and no extra disk space until files change. On Linux it reflinks where the filesystem supports it. Elsewhere it copies, up to 1 GB per command. If airbag itself fails, the command runs anyway.

## With dcg

[dcg](https://github.com/Dicklesworthstone/destructive_command_guard) blocks destructive commands it recognizes. airbag undoes the ones that get through. Run both.

## What it can't do

- It only protects what a command names, plus the project root for commands it can't see into. A script that deletes files outside your project can't be undone.
- Undo restores the snapshotted paths. Files a command created elsewhere (like the destination of `mv`) stay where they are.
- Databases, network calls, `git push` and anything outside the filesystem are out of reach.
- Variables the agent set earlier in its shell session are unknown to airbag, so commands using them fall back to the project-root snapshot.
- Windows and most Linux filesystems get plain copies, not clones.
- The Claude Code hook is tested end to end. The Codex and Cursor hooks follow their documented formats and are covered by unit tests only.

## License

[MIT](LICENSE)
