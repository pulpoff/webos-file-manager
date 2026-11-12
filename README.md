# File Manager for LG webOS

Version 0.6.0 is a file manager and media player for rooted LG webOS TVs. It was developed and tested on an LG C1 running webOS 6.5.3. It can browse:

- USB drives
- SMB shares, either found on the local network or saved as locations
- SFTP servers

It plays video, music and photos with the TV's built-in player. Thumbnails, media information and subtitles come from ffmpeg running on the TV. The app only reads files; it never changes or deletes them.

## Requirements

- **A rooted TV with the [webOS Homebrew Channel](https://github.com/webosbrew/webos-homebrew-channel).** The file service runs as root, and the Homebrew Channel starts it.
- **A 64-bit kernel** (`uname -m` prints `aarch64`), as on the LG C1 and other recent models. The file service is an arm64 binary.

## Install

Install **File Manager** from the Homebrew Channel, or install the IPK from the [releases](https://github.com/pulpoff/webos-file-manager/releases) with `ares-install`.

On first launch, the app sets itself up through the Homebrew Channel:

1. It runs `setup.sh` as root. The script installs the boot hook `/var/lib/webosbrew/init.d/98-smbbrowser` and starts the file service.
2. It offers to download ffmpeg, which is optional.

After an update, the app notices that the running file service is older than the app and runs `setup.sh` again to restart it.

### ffmpeg

Thumbnails, media information and embedded subtitles need ffmpeg. Browsing and playback work without it.

- The app offers the download once; you can also start it later from **Settings → Download ffmpeg**.
- `get-ffmpeg.sh` downloads John Van Sickle's [static arm64 build](https://johnvansickle.com/ffmpeg/) (GPLv3; 19 MB to download, about 100 MB on the TV) and checks it against the published md5.
- It installs `ffmpeg`, `ffprobe` and the GPL text into `/media/developer/smb-browser/bin/`. You can also put your own static arm64 `ffmpeg` and `ffprobe` there.
- The file service looks in that directory first, then next to `fmbridge`, then in `PATH`.

### Uninstall

Uninstall the app as usual. At the next boot, the hook sees that the app is gone. It deletes the thumbnail cache and ffmpeg, then removes itself. Your saved locations stay in `/media/developer/smb-browser/sources.json`, and the bridge's SSH key stays in `ssh/` next to it; delete that folder to remove them too.

## Components

- `app/` is what the IPK installs:
  - the TV interface: `index.html`, `css/app.css`, `js/app.js` and `assets/fa-solid-900.woff2` (Font Awesome 6.5.2 Solid, SIL OFL 1.1);
  - `appinfo.json` (app id and version) and the launcher icons `fm-icon.png` and `fm-largeIcon.png`;
  - `setup.sh`, `get-ffmpeg.sh`, the boot hook `98-smbbrowser` and the supervisor `run-bridge.sh`;
  - `bin/fmbridge`, built by `build.sh`.
- `bridge/` is the Go source of `fmbridge`, the local file service. It listens only on `127.0.0.1:31231` and does the following:
  - lists USB, SMB and SFTP sources;
  - streams files with HTTP byte ranges, so seeking works;
  - makes and caches thumbnails;
  - probes media for duration and for its video, audio and subtitle tracks;
  - extracts embedded subtitles;
  - finds SMB servers on the local network.
- `build.sh` builds the bridge, packages the IPK and writes the Homebrew Channel manifest.

The boot hook copies `run-bridge.sh` to `/media/developer/smb-browser/` and starts it. It restarts `fmbridge` 2 s after it exits for any reason, and sets an OOM score of -500 so the kernel kills other processes first.

These files live on the TV under `/media/developer/smb-browser/`:

| Path | Contents |
|---|---|
| `sources.json` | Saved locations, including passwords (mode `0600`) |
| `bin/ffmpeg`, `bin/ffprobe` | Static arm64 ffmpeg, if downloaded |
| `cache/thumbs/` | Thumbnail cache, pruned to 250 MB |
| `cache/subs/` | Extracted subtitles |
| `ssh/` | The bridge's SSH key (`id_ed25519`) and `known_hosts` |
| `server.log` | Bridge log, rotated at 1 MB |

## Build

Building needs Go (the version in `bridge/go.mod`) and Python 3. The webOS SDK is not needed.

```sh
./build.sh
```

The results are `output/com.pulp.smbbrowser_<version>_all.ipk` and `output/com.pulp.smbbrowser.manifest.json`. The version comes from `app/appinfo.json`; `build.sh` also writes it into the bridge and into `app/js/app.js`.

`build.sh` writes the IPK's `ar` container itself, the way `ares-package` does. The TV rejects archives made by GNU `ar` with error -5, "ipk verified failed".

To install a build by hand as root on the TV:

```sh
luna-send -i -f luna://com.webos.appInstallService/dev/install \
  '{"id":"com.pulp.smbbrowser","ipkUrl":"/tmp/com.pulp.smbbrowser_0.6.0_all.ipk","subscribe":true}'
```

Wait for `"state": "installed"`, then press Ctrl-C. Then open the app, which restarts the file service.

### Environment variables

These are for testing. The defaults are what the TV uses.

| Variable | Default | Purpose |
|---|---|---|
| `FM_ROOT` | `/media/developer/smb-browser` | Configuration and cache directory |
| `FM_LISTEN` | `127.0.0.1:31231` | Listen address |
| `FM_TOOLS` | `$FM_ROOT/bin` | Where to look for ffmpeg and ffprobe |
| `FM_USB_DIRS` | | Extra directories to show as USB drives, separated by colons |

## Using it

**Home** has four sections:

- **Storage** lists the USB drives, with a bar showing how full each one is.
- **Network** lists:
  - SMB servers found by scanning port 445 on the TV's /24 network, named by NetBIOS or reverse DNS;
  - saved locations;
  - **Add location**.

  A discovered server opens as a guest and lists its shares. If it needs a login, the app asks for one and saves it for that server.
- **Pinned folders**.
- **Continue watching**.

| Screen | Key | Action |
|---|---|---|
| Home | Red | Options for the selected tile |
| Home | Yellow | Refresh |
| Home | Back | Exit dialog |
| Browser | OK | Open |
| Browser | Back | Go up one folder |
| Browser | Red | Sort |
| Browser | Green | Switch between list and grid |
| Browser | Yellow | Refresh |
| Browser | Blue | Home |
| Browser | CH+/CH− | Page up or down |
| Player | Red | Audio track |
| Player | Green | Subtitles |
| Player | CH, Yellow, Blue | Previous or next file |
| Player | Left/Right | Seek (10 s, then 30 s and 120 s when held) |
| Player | Up | Audio and subtitle chips |
| Player | Down | Controls |

**Subtitles.** Embedded tracks in SRT, ASS or SSA are extracted in the background, and the app shows them as they arrive. The app also uses `.srt`, `.ass` and `.vtt` files placed next to the video. Their encoding is detected: UTF-8, UTF-16, cp1251 or cp1252.

**SFTP.** The bridge logs in with its own ed25519 key and falls back to a password. To use key login, add the bridge's public key to `~/.ssh/authorized_keys` on the server. The key is shown in the **Add location** dialog; you can also get it as root on the TV with `curl http://127.0.0.1:31231/api/sshkey`. A server's host key is trusted the first time the bridge connects.

**Stuck playback.** If playback makes no progress for 12 s, the player recovers in stages:

1. It reloads the stream at the same position.
2. It restarts the bridge (`/api/restart`) and waits for it to come back. It tries this twice.
3. It shows "Cannot play this file", with **Try again** and **Close**.

If the TV pauses playback by itself, the player resumes it after 1.5 s unless you paused.

## Bridge API

| Endpoint | Purpose |
|---|---|
| `GET /api/health` | Version, uptime and whether ffmpeg was found |
| `GET /api/sources` | `{usb:[…], network:[…]}`. Passwords are never returned. |
| `POST /api/sources/save`, `/delete`, `/test` | Manage saved locations |
| `GET /api/smb/shares` | List a server's shares |
| `GET /api/discover[?refresh=1]` | `{servers:[{host,name,login}], scanning}` |
| `GET /api/sshkey` | The bridge's public SSH key |
| `GET /api/list?src=&path=` | `{path, entries:[{name,dir,size,mtime}]}` |
| `GET /api/file?src=&path=` | File contents, with byte ranges |
| `GET /api/thumb?src=&path=&w=` | Cached JPEG thumbnail |
| `GET /api/probe?src=&path=` | Duration, video, audio and subtitle tracks, and subtitle files next to the video |
| `GET /api/subs?src=&path=&id=&from=` | `{done, cues:[[start,end,text]], total}`, filled in as extraction runs |
| `POST /api/restart` | Exit, so that the supervisor restarts the bridge |

A request is refused unless both of these hold:

- its `Host` is `127.0.0.1:31231` or `localhost:31231`;
- its `Origin`, if present, is `null` or `file://`.

This keeps web pages in other TV apps and browsers from using the bridge. Thumbnails are loaded through `localhost:31231` so they use a separate connection pool from playback and listing.

## Can it run on non-rooted webOS?

Not as it is. `fmbridge` is a native binary that runs as root, started by the Homebrew Channel. A stock TV can sideload the interface in Developer Mode, but cannot start the bridge. A non-root release would need the bridge packaged as a webOS service, or hosted on another machine such as a NAS.

## License

MIT; see `LICENSE`. Font Awesome Free is under the SIL Open Font License 1.1. ffmpeg is not part of this project: the app downloads it from its publisher on request.

## Version 0.6.0 changes

- **Sets itself up.** The first launch installs the boot hook and starts the file service through the Homebrew Channel, so a fresh install needs no shell work. After an update, the app restarts the service so the new version runs.
- **ffmpeg download.** The app offers to download a static arm64 ffmpeg, once at first launch and later from Settings.
- **Cleans up after itself.** When the app is uninstalled, the boot hook deletes the caches and ffmpeg at the next boot and removes itself, and the supervisor stops.

## Version 0.5.2 changes

- **SMB connections no longer freeze.** go-smb2 loses the SMB credits of any request cancelled while it is in flight, and the TV's player cancels range requests constantly (seeking, closing, reloading). Once the credits ran out, every call on the connection waited forever: playback stalled, folders stopped loading, and "Continue watching" did nothing. The bridge now never hands go-smb2 a request's context. Calls run on a context that lives as long as the connection, and any call without an answer after 20 s (30 s for a read) drops the connection, so the next call dials a fresh one. In a test, 300 cut-off range requests left the old bridge unable to list a folder; the new one answered in 2 ms.
- **The subtitle menu opens on videos without subtitles.** The bridge sent an empty track list as `null`, and the menu threw an error instead of opening. Empty lists are now `[]`, and the player also treats a missing list as empty.

## Version 0.5.0 changes

- **New design**, modelled on the File Manager+ Android app:
  - Home screen with Storage, Network, Pinned folders and Continue watching;
  - list and grid views, sorting, and a details pane;
  - dark and light themes.
- **Go bridge** replaces the Python bridge:
  - USB drives;
  - every share on a server, plus discovery of servers on the network;
  - SFTP;
  - a supervisor that restarts the bridge if it dies.
- **Thumbnails** for photos, videos and album art, made by ffmpeg on the bridge at low priority and cached on disk, so the interface never waits for them. Video previews no longer use a hidden `<video>` element: a second decoder took the TV's hardware decoder away from the player and stopped playback.
- **Player**:
  - automatic recovery from stuck playback;
  - audio and subtitle chips in the top-right corner;
  - embedded and external subtitles;
  - resume position;
  - "Up next" countdown;
  - full-screen photo viewer.
- Light blue Font Awesome folder icon. The icon files have new names so the launcher does not show its cached copy of the old orange icon.
