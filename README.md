# recents

Recently opened files, in your terminal. A daemon watches file opens
system-wide (fanotify), keeps only the ones *you* made — media, documents,
images — and a TUI lists them. Like the Windows Explorer "Recent files" view,
for everything.

Which episode was I on? `recents` → top of the list.

Linux only.

## Install

Requires Go 1.23+ and systemd.

```sh
git clone https://github.com/kaleabSolomon/recents.git
cd recents
sudo make install
```

That builds both binaries, installs them to `/usr/local/bin`, and starts the
`recentsd` service tracking the user who ran the command
(`RECENTS_USER=name` to override). `sudo make uninstall` removes everything.

## Use

```sh
recents
```

| Key | |
| --- | --- |
| `j`/`k` `g`/`G` | move / top / bottom |
| `Enter` / `Ctrl+o` | open file / its folder |
| `/` | search |
| `f` | filter: `mkv,mp4` or `video` `audio` `documents` `images` `code` |
| `d` | folder view — latest file per directory |
| `Esc` | clear |
| `q` | quit |

## Config

`~/.config/recents/config.toml` — optional, defaults shown trimmed:

```toml
watch_paths = ["~/"]
ignored_paths = ["node_modules", ".git", ".cache"]
tracked_extensions = ["mkv", "mp4", "pdf", "epub", "png", "jpg"]
max_entries = 50000
```

An open is recorded only if the path matches the config, the opening process
is yours with a terminal or display session (indexers and thumbnailers are
rejected), and it isn't part of a mass-open burst. Files appear a few seconds
after you open them.

## Development

```sh
make ci    # fmt + vet + test + build
```
