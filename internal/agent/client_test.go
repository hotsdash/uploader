package agent

import (
	"testing"
	"time"
)

func TestIsGameProcess(t *testing.T) {
	yes := []string{
		"HeroesOfTheStorm_x64.exe", "heroesofthestorm.exe", "HeroesOfTheSto\n",
		`C:\Program Files (x86)\Heroes of the Storm\Versions\Base91756\HeroesOfTheStorm_x64.exe`,
		"Z:/games/hots/Versions/Base91756/HeroesOfTheStorm_x64.exe",
		"/Applications/Heroes of the Storm/Versions/Base91756/Heroes.app/Contents/MacOS/Heroes",
	}
	no := []string{"", "Battle.net.exe", "Heroes of the Storm.exe", "HeroesSwitcher_x64.exe", "Agent.exe", "heroes", "wine64-preloader", "/usr/bin/HeroesOfTheStormTool"}
	for _, n := range yes {
		if !isGameProcess(n) {
			t.Errorf("%q should be the game", n)
		}
	}
	for _, n := range no {
		if isGameProcess(n) {
			t.Errorf("%q isn't the game", n)
		}
	}
}

func TestNextClient(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 20, 0, 0, 0, time.UTC)
	c, changed := nextClient(ClientState{}, false, false, t0)
	if changed || c.Known {
		t.Fatalf("a failed first look says nothing: %+v", c)
	}
	c, changed = nextClient(c, false, true, t0)
	if !changed || !c.Known || c.Open || !c.Since.Equal(t0) {
		t.Fatalf("first look: %+v", c)
	}
	c, changed = nextClient(c, false, true, t0.Add(time.Minute))
	if changed || !c.Since.Equal(t0) {
		t.Fatalf("no change keeps since: %+v", c)
	}
	c, changed = nextClient(c, true, true, t0.Add(2*time.Minute))
	if !changed || !c.Open || !c.Since.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("opened: %+v", c)
	}
	c, changed = nextClient(c, false, false, t0.Add(3*time.Minute))
	if changed || !c.Open {
		t.Fatalf("a failed look keeps it open: %+v", c)
	}
}

func TestClientReport(t *testing.T) {
	if clientReport(ClientState{}) != nil {
		t.Fatal("unknown sends nothing")
	}
	r := clientReport(ClientState{Known: true, Open: true, Since: time.Date(2026, 11, 15, 20, 0, 0, 0, time.FixedZone("x", 3600))})
	if r["open"] != true || r["since"] != "2026-11-15T19:00:00Z" {
		t.Fatalf("%v", r)
	}
}
