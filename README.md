# subclerk

Music queue and rating manager for [Navidrome](https://www.navidrome.org/) with local playback via [mpv](https://mpv.io/).

Spiritual successor to [clerk](https://github.com/carnager/clerk-modular), replacing MPD with Navidrome's Subsonic API.

## Components

- **subclerkd** — Daemon that manages mpv playback, maintains a local library cache, and exposes an HTTP API (Unix socket + optional TCP). Includes an embedded web UI.
- **subclerk-tui** — Terminal UI (Bubble Tea) with library browser, queue management, ratings, and search.
- **subclerkc** — Minimal CLI for playback control (`prev`, `toggle`, `stop`, `next`, `update`, `status`).
- **subclerk-rofi** — Rofi/dmenu client for album/track selection.
- **subclerk-musiclist** — Static music list exporter.

## Features

- Browse artists, albums, and tracks from Navidrome
- Queue management with drag-and-drop (web) and multi-select (TUI)
- Track and album ratings (1–10)
- Search across albums and tracks
- Random album / random tracks
- Last.fm scrobbling
- Smooth seekbar with interpolation (web UI)
- Clickable seekbar (TUI)
- Keyboard-driven with comprehensive hotkeys

## Build

```sh
./build
```

Binaries are placed in `bin/`.

Requires Go 1.24+.

## Configuration

Config file: `$XDG_CONFIG_HOME/subclerk/subclerk.toml`

```toml
[navidrome]
url = "http://localhost:4533"
user = "your-user"
password = "your-password"

[mpv]
# extra_args = ["--audio-device=pulse"]

[addresses]
local = "auto"
# remote = "hostname:9444"

[random]
tracks = 20

[scrobble]
enabled = false
# api_key = ""
# api_secret = ""
# session_key = ""
```

## Systemd

```sh
cp subclerkd/subclerkd.service ~/.config/systemd/user/
systemctl --user enable --now subclerkd
```

## License

MIT
