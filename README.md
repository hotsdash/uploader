# hotsdash uploader

The replay uploader for [hotsdash.com](https://hotsdash.com), a Heroes of the Storm stats dashboard. It's a small
program that sits by the clock (Windows), or runs in the background (Linux, macOS), finds your replay folders, uploads
every replay once, keeps uploading new ones as you play, and tells you who you've met before when a game loads. It
keeps itself up to date.

A replay only counts as sent once the dashboard has accepted it. One it turns down is tried again after a minute,
10 minutes, an hour and 6 hours; after the fifth refusal it's left alone (until the file changes), and Recent activity
says why.

This is the whole program: a single Go module with no cgo, built for Windows, Linux (x86-64 and ARM) and macOS
(one universal program).

## Install

Download it from your dashboard's Uploader panel (or from the latest release here) and run it. It installs itself for
your user, starts, and starts again when you log in. Run the installed copy again for its settings page.
To remove it: `hots-dash-uploader --uninstall` (Linux and macOS).

The Linux and macOS downloads are a `.tar.gz` holding the program (a browser drops the "can run" bit of a bare
program). On macOS, run `xattr -dr com.apple.quarantine hots-dash-uploader` next to it, or install with the one-line
command from the dashboard, which isn't quarantined.

Every download of a version is the same file for everyone, byte for byte.

## How sign-in works

Nothing is set up in the download. The uploader uploads to `https://hotsdash.com` by default; to use another
dashboard (self-hosting), change the address in its settings page, or start it with `-server https://your.dashboard`.

The first time it runs (and isn't signed in) it signs in like a TV app:

1. It asks the dashboard for a short code (`POST /api/agent/link`).
2. It opens your browser on the dashboard's approval page, `…/link?code=ABCD-EFGH`: the code is in the address, so
   there's nothing to type. The page must be on the dashboard's own address, or it isn't opened.
3. You click **Approve** in the browser where you're already signed in to the dashboard.
4. The uploader, which has been polling with a secret only it knows, receives your upload token and keeps it in
   `config.json`.

It never reads browser cookies or anything else from your browser. Skip it and the uploader still uploads, anonymously;
press **Sign in** in its settings page any time. The sign-in is only offered automatically once.

Copies installed before this repository existed carry on as they were: the saved `config.json` (and its token) is read
as before, and the old settings appended to the `.exe` (a `HDCFG001` trailer) or the `hots-dash-setup.json` beside the
program are still understood.

## Where things are

| | Windows | Linux | macOS |
|---|---|---|---|
| program | `%LOCALAPPDATA%\Programs\hots-dash\` | `~/.local/bin/hots-dash-uploader` | app folder (`~/Library/Application Support/hots-dash`) |
| settings (`config.json`), state, log | `%APPDATA%\hots-dash` | `~/.config/hots-dash` | `~/Library/Application Support/hots-dash` |

(See `tray_windows.go` and `platform_unix.go` for the exact paths and the autostart entries.)

## Build

Go 1.26 or newer; no other tools.

```sh
go vet ./...
go test ./...
go build .                    # for this computer
scripts/build.sh              # every platform into dist/ (VERSION=2026.11.25.1430 to set the version)
```

Versions are the build time in UTC, `YYYY.MM.DD.HHMM`; a bigger one is newer. A build from source is version `dev`,
which never updates itself.

## Updates and how releases are verified

Installed copies don't look at GitHub. Every few hours they ask the dashboard they upload to (`{server}/api/uploader/version`)
what the newest version is, and download it from `{server}/api/uploader/update`, which the dashboard may redirect to a
release file here (the uploader follows redirects only to https on the dashboard's own host, `github.com` and
`*.githubusercontent.com`). Whoever serves the bytes, the copy refuses the update unless all of these hold:

- the size and SHA-256 match what the dashboard announced;
- the Ed25519 signature over the version and that SHA-256 (`updatesig.Message`) verifies against the public key built
  into the running program (`main.updateKey`);
- the file is a program for this system.

So the dashboard decides what's "latest" (and can hold a release back or roll back), but can't hand out a program that
wasn't signed with the release key.

A release (`Actions → Release`, run by hand) builds everything with `-trimpath`, signs each program with the key in the
`UPLOADER_SIGNING_KEY` secret (a base64 seed made by `go run ./tools/sign keygen`; the release fails without it), and
publishes, under the tag `v<version>`:

| asset | what |
|---|---|
| `hots-dash-uploader.exe` | Windows: the program, and the first download |
| `hots-dash-uploader-linux-amd64`, `-linux-arm64`, `-macos` | the bare programs the updater fetches |
| `hots-dash-uploader-linux-amd64.tar.gz`, `-linux-arm64.tar.gz`, `-macos.tar.gz` | first downloads: the program in a folder, made the same every time (fixed dates and owner) |
| `latest.json` | the version and, per system, the program's name, size, SHA-256 and Ed25519 signature, and the download file |

```json
{"version":"2026.11.25.1430","files":{"windows":{"program":{"name":"hots-dash-uploader.exe","size":0,"sha256":"…","signature":"…"},"download":"hots-dash-uploader.exe"}, "linux-amd64":{…}, "linux-arm64":{…}, "macos":{…}}}
```

To check a download yourself: compare its SHA-256 with `latest.json`, and verify the signature over
`updatesig.Message(version, sha256)` with the public key (`go run ./tools/sign pub -key KEY` prints it; the key is built
into every release's program as `main.updateKey`).

Windows code signing (Authenticode) isn't set up yet; the place for it in `release.yml` is marked.
