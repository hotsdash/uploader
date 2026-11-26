**DRAFT: needs human/legal review**

# Privacy and the hotsdash uploader

This is what the uploader on your computer sends, where it goes, and how to turn things off. It doesn't cover what you
do on the website.

**TODO (dashboard owner):** who runs hotsdash.com and is responsible for this data, how to contact them, the date this
notice takes effect, and a link to the website's own privacy policy.

## The short version

- It uploads your Heroes of the Storm replays. It starts as soon as you run it, including the replays you already
  have, and you don't need to sign in. On a new install the sign-in page only opens after that first pass.
- Replays and lobby files have **other players' information** in them, not just yours.
- The live game check is on to begin with. It sends a file about each game as it loads.
- When it asks you to sign in, it sends your computer's name. The first time, it asks by itself.
- Signed in, it also tells the dashboard whether Heroes is running.

## What gets sent

| Data | When | Signed-out too? |
|---|---|---|
| Replay files (contain all 10 players' battletags, chat, etc.) | Every new replay | Yes (anonymous) |
| Path from the Battle.net account folder down (`2-Hero-1-…\Replays\…`) | Upload + `replays/known` | Yes |
| Full lobby file (base64) + all battletags in it, region | Each game load | Yes |
| Rejoin saves | During games (if set up) | Yes |
| Computer hostname + account IDs + replay counts | Sign-in start | Yes (anonymous call) |
| Whether Heroes is running, and since when | Heartbeat (every 60 s) | Signed-in only |
| Live-folder stats, timing and clock offset, user agent (version/OS/arch) | Heartbeat / uploads | Mixed |

**"Anonymous" means no dashboard account is attached.** It doesn't mean nothing points to you: replays have your
battletag in them, the path has your Battle.net account ID, and the dashboard sees the internet address you connect
from. The settings page says replays uploaded while signed out are "uploaded for everyone to see".

**Signed in**, your upload token goes with what the uploader sends (not with the requests that sign you in), so the
dashboard knows it's you. Replays uploaded from an account's folder link that Battle.net account to you, unless you've
said it isn't yours.

### Replays and their paths

"Every new replay" means every replay the dashboard doesn't have yet, in every replay folder that's switched on. That
includes your old games and any backup folders you've added. The files are sent whole.

Before uploading, the uploader asks which ones the dashboard already has (`replays/known`). It sends a fingerprint
(SHA-256 hash) of each file and its path. The path starts at the Battle.net account folder; a replay that isn't in one
is sent by file name only. The part above, which often holds your computer user name, isn't sent as a replay's path.

Each upload also says when the file was last changed, when and how the uploader first saw it, when it started sending,
and how far your computer's clock is from the dashboard's.

### Live game check: the lobby file

While **Check games as they load** is on (it is to begin with), then each time a game loads the uploader sends the
game's whole lobby file, the battletags it finds in it, and your region (when your replay folders point to just one).
The dashboard reads things like parties, levels and the map from the file. The same players again within 10 minutes
aren't sent twice. If a game loads while the switch is off, its lobby file may still be sent when you switch it back on.

On Linux this only happens if the uploader can find the game's Wine folder, or you name the lobby folder yourself.

### Live game check: rejoin saves

While a game is on, Heroes writes a rejoin save so you can reconnect. It says which hero everyone picked. The uploader
sends the newest one, whole, and may send newer ones as the game goes on. It asks the dashboard first, so that it can
skip this when another player's uploader is already sending them.

"If set up" doesn't mean you have to set it up yourself: **the uploader switches on the rejoin folder for your main
account automatically** (the one with the most replays). Rejoin saves are only sent while **Check games as they load**
is on.

### Signing in

See [your computer's name](#your-computers-name) below. Along with it go the ID of each Battle.net account found in
your replay folders and how many replays each has.

### The check-in every minute

Signed in, the uploader checks in when it starts, every 60 seconds, and when Heroes opens or closes. It sends:

- whether Heroes is running, and since when the uploader has seen it that way;
- whether **Check games as they load** is on;
- if it is: how many Battle.net accounts were found, how many have a rejoin folder switched on, and when the newest
  rejoin save was written. For that last one it also looks in the game's own rejoin folders that aren't switched on.

Signed out, there's no check-in.

### Other things it asks the dashboard

- **Who your account folders are:** while the settings page is open, it sends your Battle.net account IDs to get their
  battletags.
- **How a game went:** after uploading a game that just finished, it asks for the summary, even if you've turned the
  notification off.
- **New versions:** shortly after starting and every 6 hours, it asks for the newest version, naming your operating
  system and processor type.

Requests to the dashboard carry a "user agent": the uploader's version, your operating system and processor type.

## Other people's information

Replays hold all 10 players' battletags, the game's chat and more. Lobby files and rejoin saves are about everyone in
the game too. Those players haven't installed anything, and the uploader doesn't ask them.

The uploader also shows you things about them, such as who you've played with before. Those names can appear in
notifications and in the log on your computer.

**TODO (dashboard owner):** what the dashboard does with other players' information, what others can see about them,
and how someone who doesn't use the uploader can ask to see or remove it.

## Checking whether Heroes is running

Every 5 seconds the uploader looks through the list of programs running on your computer for the game's name. On
Windows it reads a snapshot of the process list, on Linux each process's name and the program it was started as (from
`/proc`), and on macOS the output of `ps`.

The list stays on your computer. Only "open or not, and since when" is sent, and only while you're signed in. The check
itself runs either way and has no switch of its own.

## Your computer's name

When a sign-in starts, the uploader sends your computer's name (its hostname) so the approval page can show which
computer is asking. Computer names often contain a person's name, such as "Alex-PC".

It's sent when the sign-in **starts**, so it goes even if you close the page without approving. The first time the
uploader runs, it starts a sign-in by itself. After that, only when you press **Sign in**.

## Where it goes

Your replays, lobby files, rejoin saves and sign-in details are sent to **one place: the dashboard the uploader is set
to use**. That's `https://hotsdash.com` unless you, or the download you installed, chose another. If you use someone
else's dashboard, they receive it all, and the answers in this notice about hotsdash.com don't apply to them. If its
address is plain `http`, none of it is encrypted on the way.

If the dashboard says it has moved, the uploader checks that the new address answers, without sending your sign-in or
files. It asks you before switching. The one exception: moving from `http` to `https` on the same address happens by
itself.

Other places the uploader may connect to:

- **GitHub**, to download an update. GitHub sees your internet address and the user agent.
- **Cloudflare and Google**, only for the network workaround below.

**TODO (dashboard owner):** the companies that host the dashboard or handle its traffic (for example Cloudflare, which
hotsdash.com is reached through), which countries the data is kept in, and whether it's shared with anyone else.

## Networks that block hotsdash.com

Some networks answer for `hotsdash.com` with a block page of their own. The setting **Get around networks that block
hotsdash.com** is on to begin with. It only does something when that happens:

- it looks the address up over encrypted DNS (DNS-over-HTTPS), asking Cloudflare at `1.1.1.1`, then Google at
  `8.8.8.8` if that fails. They see your internet address and that you're looking up `hotsdash.com`;
- it connects there, hiding the name from your network with Encrypted Client Hello (ECH) where it can;
- these lookups and connections don't go through a proxy you've set up.

It's only used for `hotsdash.com`, and **certificates are still checked**, so it only ever reaches the real dashboard.

This gets around a block that your network, security software or provider may have put there on purpose. Switch it off
if you don't want that.

## Turning things off

The switches are on the uploader's settings page. On Windows, open it from the hots-dash icon by the clock; on Linux
and macOS, run the installed `hots-dash-uploader`.

| To stop | Do this |
|---|---|
| Uploading replays from a folder | Under **Replay folders**, switch off **Use**. To stop all replay uploads, switch off every folder. Switch them off rather than removing them: with no folders in the list, the uploader looks for the game's folders again next time it starts |
| Lobby files and rejoin saves | Switch off **Check games as they load** |
| Rejoin saves for one account | Under **Live game folders**, press **Remove** beside it. For a folder under "Other rejoin folders", you can switch it off instead |
| The check-in (Heroes running, live folder numbers) | Press **Sign out**. Replays and the live game check carry on without your account. There's no separate switch |
| Sending your computer's name again | Don't press **Sign in**. The sign-in on first run can't be turned off beforehand |
| Going around network blocks | Switch off **Get around networks that block hotsdash.com** |
| Updating by itself | Switch off **Update automatically**. It still checks for new versions |
| Game result notifications | Switch off **Show how each game went (personal bests, losing runs)**. This only hides the notification |
| Everything | On Windows, switch off **Start with Windows** (or it starts again at your next log-in), then choose **Quit** from the tray icon. On Linux and macOS, run `hots-dash-uploader --uninstall` |

Turning something off, signing out or uninstalling doesn't delete anything already uploaded.

## What stays on your computer

The uploader keeps its settings (including your upload token), a record of which replays it has sent, and a log. The
log holds folder paths and recent activity, which can include other players' names. It isn't uploaded.

They're in `%APPDATA%\hots-dash` on Windows, `~/.config/hots-dash` on Linux and
`~/Library/Application Support/hots-dash` on macOS. Uninstalling on Linux or macOS leaves the settings and log behind;
delete that folder to remove them. The uploader never changes or deletes your replays, and it doesn't read your
browser's cookies.

**TODO (dashboard owner):** how to remove the program completely on Windows (there's no uninstall command).

## What the dashboard does with it

The uploader's code can't answer these. They need filling in before this notice is used.

- **TODO (dashboard owner):** why each kind of data is collected and the legal basis for it.
- **TODO (dashboard owner):** how long each is kept: replays, paths and account IDs, lobby files, rejoin saves,
  computer names (including from sign-ins never approved), check-in data, timing data and server logs (including
  internet addresses).
- **TODO (dashboard owner):** who can see what's uploaded, signed in and signed out, including replay chat.
- **TODO (dashboard owner):** how to see, download, correct or delete your data, and what happens when an account is
  closed.
- **TODO (dashboard owner):** whether signing out should also cancel the upload token on the dashboard, and how to do
  that. Signing out only makes this computer forget it.
- **TODO (dashboard owner):** any age limit, and how changes to this notice are announced.
