package yandex

import (
	"strings"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

func TestLaneTagRoundTrip(t *testing.T) {
	in := laneTag{role: "client", group: "1a2b3c4d", start: 1760160000123, index: 3}
	out, ok := parseLaneTag(in.String())
	if !ok || out != in {
		t.Fatalf("%q parsed as %+v, %v", in.String(), out, ok)
	}
	if s := in.String(); strings.Contains(strings.ToLower(s), "openflux") || len(s) > 24 {
		t.Errorf("tag %q", s)
	}
	for _, tag := range []laneTag{in, {role: "exit", group: "ffffffff", start: 1<<47 - 1, index: 63}, {role: "client", group: "00000000", start: 1, index: 0}} {
		name := participantName + tag.hidden()
		got, ok := parseHiddenTag(name)
		if !ok || got != tag {
			t.Errorf("hidden %+v came back as %+v, %v", tag, got, ok)
		}
		visible := strings.Map(func(r rune) rune {
			for _, m := range tagMarks {
				if r == m {
					return -1
				}
			}
			return r
		}, name)
		if visible != participantName {
			t.Errorf("the name shows as %q", visible)
		}
	}
	for _, n := range []string{"", participantName, "Гость\u200b", participantName + in.hidden()[:20]} {
		if _, ok := parseHiddenTag(n); ok {
			t.Errorf("%q parsed as a hidden tag", n)
		}
	}
	for _, s := range []string{"", "Гость", "c1a2b3c4d", "c1a2b3c4d-1", "x1a2b3c4d-mg3x8k2a-0", "c1a2b3c4-mg3x8k2a-0",
		"c1a2b3c4z-mg3x8k2a-0", "c1a2b3c4d-!-0", "c1a2b3c4d-0-0", "c1a2b3c4d-mg3x8k2a--1", "c1a2b3c4d-mg3x8k2a-0-1"} {
		if _, ok := parseLaneTag(s); ok {
			t.Errorf("%q parsed as a lane tag", s)
		}
	}
}

// After a client reconnects the room still lists its previous lanes; an exit
// watches only the newest client group, lane i on lane i.
func TestGroupWatchesTheNewestPeerGroup(t *testing.T) {
	g := NewTelemostGroup("", "https://telemost.yandex.ru/j/1", 3, true, transport.DefaultConfig())
	now := time.Now()
	parts := map[string]tmPart{}
	add := func(id string, tag laneTag) {
		parts[id] = tmPart{streaming: true, since: now.Add(-time.Minute), tag: tag, tagged: true}
	}
	for i := 0; i < 3; i++ {
		add("old-"+string(rune('a'+i)), laneTag{role: "client", group: "old", start: 100, index: i})
		add("new-"+string(rune('a'+i)), laneTag{role: "client", group: "new", start: 200, index: i})
		add("exit-"+string(rune('a'+i)), laneTag{role: "exit", group: "ghost", start: 300, index: i})
	}
	parts["someone"] = tmPart{streaming: true, since: now.Add(-time.Minute)}
	g.mu.Lock()
	for _, l := range g.lanes {
		l.lane.parts = parts
	}
	g.mu.Unlock()
	g.recompute()
	for i, l := range g.lanes {
		l.lane.mu.Lock()
		target := l.lane.target
		l.lane.mu.Unlock()
		if want := "new-" + string(rune('a'+i)); target != want {
			t.Errorf("lane %d watches %q, want %q", i, target, want)
		}
	}
}
