package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// Heroes has no Linux version: people play it through Wine (Lutris, Bottles, Steam's Proton, Heroic, or plain Wine),
// so the game's Documents folder is inside a Wine "prefix", a pretend C: drive somewhere in their home folder. These
// are the usual places for one. Each is a glob for the prefix (the folder holding drive_c).
func winePrefixGlobs(home string) []string {
	g := []string{
		".wine",
		"Games/*",   // Lutris
		"Games/*/*", // Lutris with a runner folder, Heroic's Prefixes/<game>
		".local/share/lutris/runners/wine/*",
		".local/share/bottles/bottles/*", // Bottles
		".var/app/com.usebottles.bottles/data/bottles/bottles/*",
		".local/share/Steam/steamapps/compatdata/*/pfx", // Steam (Proton)
		".steam/steam/steamapps/compatdata/*/pfx",
		".var/app/com.valvesoftware.Steam/.local/share/Steam/steamapps/compatdata/*/pfx",
		".var/app/net.lutris.Lutris/data/lutris/runners/wine/*",
		"Faugus/*", // Faugus Launcher
		".local/share/faugus-launcher/prefixes/*",
	}
	out := make([]string, 0, len(g)+1)
	if p := os.Getenv("WINEPREFIX"); p != "" {
		out = append(out, p)
	}
	for _, p := range g {
		out = append(out, filepath.Join(home, p))
	}
	return out
}

// wineReplayFolders lists Heroes' Accounts folders inside the Wine prefixes found under home.
func wineReplayFolders(home string) []string {
	var out []string
	for _, prefix := range winePrefixGlobs(home) {
		for _, docs := range []string{"Documents", "My Documents"} {
			m, _ := filepath.Glob(filepath.Join(prefix, "drive_c", "users", "*", docs, "Heroes of the Storm", "Accounts"))
			out = append(out, m...)
		}
	}
	return out
}

// wineUser: the Windows user folder (…/drive_c/users/<name>) a path inside a Wine prefix belongs to ("" if it isn't).
func wineUser(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "drive_c" && strings.EqualFold(parts[i+1], "users") {
			return filepath.FromSlash(strings.Join(parts[:i+3], "/"))
		}
	}
	return ""
}

// wineLobbyDir: where the game, running in Wine, writes the lobby file: its own %TEMP%\Heroes of the Storm, inside
// the prefix of the first replay folder that's in one. "" when none is.
func wineLobbyDir(folders []Folder) string {
	for _, f := range folders {
		u := wineUser(f.Path)
		if u == "" {
			continue
		}
		for _, tmp := range []string{filepath.Join(u, "AppData", "Local", "Temp"), filepath.Join(u, "Temp"), filepath.Join(u, "Local Settings", "Temp")} {
			if st, err := os.Stat(tmp); err == nil && st.IsDir() {
				return filepath.Join(tmp, "Heroes of the Storm")
			}
		}
		return filepath.Join(u, "AppData", "Local", "Temp", "Heroes of the Storm")
	}
	return ""
}

// lobbyDir: the folder the game writes the lobby file in. set is the one saved or chosen, if any. On Linux the game
// only runs in Wine, so without a prefix there's none (""): never this computer's /tmp, which anyone can write to.
func lobbyDir(goos, set string, folders []Folder) string {
	own := filepath.Join(os.TempDir(), "Heroes of the Storm")
	// earlier versions defaulted to /tmp on Linux and saved it with the settings
	if goos == "linux" && (set == own || set == "/tmp/Heroes of the Storm") {
		set = ""
	}
	if set != "" {
		return set
	}
	// in Wine the game's %TEMP% is inside its prefix, not this computer's own
	if d := wineLobbyDir(folders); d != "" {
		return d
	}
	if goos == "linux" {
		return ""
	}
	return own
}
