# recents

A "recently opened files" tracker for the Linux terminal — like the Recent
Files view in Windows Explorer, but for everything you open, in a TUI.

A root daemon (`recentsd`) watches file opens system-wide via fanotify,
filters them down to files *you* deliberately opened (media, documents,
images by default), and records them in SQLite. The `recents` TUI shows the
list — searchable, filterable, and able to reopen a file or its folder.

Typical use: "which episode was I on?" — open `recents`, the file at the top
of the list is your answer.

## Install (always-on daemon)

```sh
sudo make install      # builds, installs to /usr/local/bin, enables systemd unit
systemctl status recentsd
```

The unit tracks the user who ran `sudo make install`. For a different user:
`sudo make install RECENTS_USER=name`. Remove everything with
`sudo make uninstall`.

To run the daemon by hand instead: `sudo ./bin/recentsd` (sudo attribution
works via SUDO_UID).

## TUI

```sh
recents
```

| Key | Action |
| --- | --- |
| `j`/`k`, arrows | move |
| `g` / `G` | top / bottom |
| `Enter` | open file (xdg-open) |
| `Ctrl+o` | open containing folder |
| `/` | live search by name |
| `f` | filter by extension (`mkv,mp4`) or category (`video`, `audio`, `documents`, `images`, `code`) |
| `Esc` | clear filter / exit input |
| `r` | refresh (also auto-refreshes every 2s) |
| `q` | quit |

## Config

`~/.config/recents/config.toml` (all optional):

```toml
watch_paths = ["~/"]
ignored_paths = ["node_modules", ".git", ".cache"]
tracked_extensions = ["mkv", "mp4", "pdf", "epub", "png", "jpg"]
max_entries = 50000
```

## How opens are filtered

fanotify reports every open on the watched mounts, so the daemon filters
aggressively; an open is recorded only if:

- the path is under `watch_paths`, not ignored, and has a tracked extension
- the opening process belongs to you and has a terminal or a display session
  (background indexers, thumbnailers, and daemons are rejected)
- the process isn't mass-opening files: opens are held for ~2s, and a process
  that opens more than 3 distinct tracked files in that window has the whole
  burst discarded (repeat opens of the same file don't count)

Net effect: a file appears in the TUI a few seconds after you open it.

## Development

```sh
make ci     # fmt + vet + test + build
make perf   # benchmarks
```
