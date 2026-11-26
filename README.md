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
dashboard (self-hosting) on Linux or macOS, start it with `-server https://your.dashboard`, which runs it in that
terminal.

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

## Running it with your own token

With `-server`, the uploader runs in the terminal and takes an upload token from the `HOTSDASH_TOKEN` environment
variable (Linux and macOS):

```sh
read -rs HOTSDASH_TOKEN && export HOTSDASH_TOKEN    # paste the token; it isn't shown or kept in shell history
hots-dash-uploader -server https://your.dashboard
```

`--token` still works, but other programs on the computer can see a command line, so it prints a warning.

## Where things are

| | Windows | Linux | macOS |
|---|---|---|---|
| program | `%LOCALAPPDATA%\Programs\hots-dash\` | `~/.local/bin/hots-dash-uploader` | app folder (`~/Library/Application Support/hots-dash`) |
| settings (`config.json`), state, log | `%APPDATA%\hots-dash` | `~/.config/hots-dash` | `~/Library/Application Support/hots-dash` |

On Linux and macOS the settings folder and the log are readable by you only.

(See `tray_windows.go` and `platform_unix.go` for the exact paths and the autostart entries.)

On Linux the game runs in Wine, so the check on who's in a game looks for the lobby file inside the Wine prefix your
replay folder is in, found when the uploader starts. With no replay folder in a prefix there's no check; `-lobby DIR`
names the folder yourself (anything but `/tmp/Heroes of the Storm`, which is never used).

## When the dashboard moves

The version answer can name the dashboard's own address. If that's a different one, the uploader checks something
answers there and then asks you in Settings before switching (the step from http to https on the same address switches
by itself). It only tries a public https name, never an IP address, `localhost`, a `*.local` name or one without a dot,
and each address no more than once a day. To use such an address, start the uploader with `-server`.

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
`*.githubusercontent.com`). When the version answer carries the `url` of a release file of this project, that is
fetched first; if it can't be had (an error, or anything but 200), `{server}/api/uploader/update` is asked once instead.
Whoever serves the bytes, the copy refuses the update unless all of these hold:

- the size and SHA-256 match what the dashboard announced;
- an Ed25519 signature over the version and that SHA-256 verifies against one of the public keys built into the
  running program (`main.updateKey`). When the dashboard passes on `signatureV2`, which names the platform as well
  (`updatesig.MessageV2`), that one has to verify for the running system; without it, `signature`
  (`updatesig.Message`) does;
- the file is a program for this system.

So the dashboard decides what's "latest" (and can hold a release back or roll back), but can't hand out a program that
wasn't signed with a trusted key.

A copy with a version but no key built in (e.g. `scripts/build.sh` without `UPLOADER_SIGNING_KEY` or `UPLOADER_PUBKEY`)
can't check signatures, so it never updates itself: it says a new version is ready, to be downloaded from the
dashboard. Only a build from source (`dev`) goes without the signature check, and it never updates by itself.

### The two keys

A release has two public keys built in, comma-separated in `main.updateKey`:

- the **release key**: `UPLOADER_PUBKEY` (a repository variable), the public half of the `UPLOADER_SIGNING_KEY` secret
  that signs releases. Without the variable it's worked out from the secret; with it, the two have to match;
- the **backup key**: `UPLOADER_BACKUP_PUBKEY` (a repository variable, required). Its private half is kept offline,
  away from GitHub, and only used if the release key is lost or leaked.

If a key is compromised, retire it with a release signed by the other:

1. Make a new key pair (`go run ./tools/sign keygen`, then `pub`) and keep it offline.
2. If it's the release key: set the `UPLOADER_SIGNING_KEY` secret to the backup private key, `UPLOADER_PUBKEY` to the
   backup public key and `UPLOADER_BACKUP_PUBKEY` to the new public key. If it's the backup key: only set
   `UPLOADER_BACKUP_PUBKEY` to the new public key.
3. Release. Copies that update to it no longer trust the old key; copies that haven't updated yet still do, so get
   the dashboard to hand it out promptly.

Copies from before there was a backup key (`2026.11.24.…`) trust only the first release key: they can only be moved on
by a release signed with it, or by downloading the new version again.

A release (`Actions → Release`, run by hand) builds everything with `-trimpath`, signs each program with the key in the
`UPLOADER_SIGNING_KEY` secret (a base64 seed made by `go run ./tools/sign keygen`; the release fails without it, or
without `UPLOADER_BACKUP_PUBKEY`), and
publishes, under the tag `v<version>`:

| asset | what |
|---|---|
| `hots-dash-uploader.exe` | Windows: the program, and the first download |
| `hots-dash-uploader-linux-amd64`, `-linux-arm64`, `-macos` | the bare programs the updater fetches |
| `hots-dash-uploader-linux-amd64.tar.gz`, `-linux-arm64.tar.gz`, `-macos.tar.gz` | first downloads: the program in a folder, made the same every time (fixed dates and owner) |
| `latest.json` | the version and, per system, the program's name, size, SHA-256 and Ed25519 signatures, and the download file |

```json
{"version":"2026.11.25.1430","files":{"windows":{"program":{"name":"hots-dash-uploader.exe","size":0,"sha256":"…","signature":"…","signatureV2":"…"},"download":"hots-dash-uploader.exe"}, "linux-amd64":{…}, "linux-arm64":{…}, "macos":{…}}}
```

Each program is signed twice (`scripts/sign.sh`):

| signature | signs these lines (each ends with a newline; the hash in lower-case hex) |
|---|---|
| `signature` (v1, `<program>.sig`), for copies already installed | `hots-dash-uploader update` / `version <version>` / `sha256 <hash>` |
| `signatureV2` (`<program>.sig2`) | `hots-dash-uploader update v2` / `version <version>` / `platform <platform>` / `sha256 <hash>` |

The platform is the program's key in `latest.json`: `windows`, `linux-amd64`, `linux-arm64` or `macos`.

To check a download yourself: compare its SHA-256 with `latest.json`, and verify a signature over
`updatesig.Message(version, sha256)` or `updatesig.MessageV2(version, sha256, platform)` with the release public key (`go run ./tools/sign pub -key KEY` prints it; it is
built into every release's program, first in `main.updateKey`).

Windows code signing (Authenticode) isn't set up yet; the place for it in `release.yml` is marked.
