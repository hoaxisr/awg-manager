package subscription

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

func TestApplyDiff_DistinctUsernamesWithSharedPassword(t *testing.T) {
	for _, scheme := range []string{"naive+http", "naive+https"} {
		t.Run(scheme, func(t *testing.T) {
			var parsed []vlink.ParsedOutbound
			var links []string
			for _, username := range []string{"alice", "bob", "alice"} {
				link := fmt.Sprintf("%s://%s:shared-test-password@proxy.example:1080", scheme, username)
				p, err := vlink.ParseLink(link)
				if err != nil {
					t.Fatalf("parse test link: %v", err)
				}
				parsed = append(parsed, *p)
				links = append(links, link)
			}

			// An account that was already distinguishable keeps its stored tag.
			legacyTag := StableTag("accounts", parsed[0])
			single := ApplyDiff("accounts", []string{legacyTag}, []vlink.ParsedOutbound{parsed[0], parsed[2]})
			if len(single.Existing) != 1 || len(single.New) != 0 || len(single.Orphan) != 0 || single.SkippedDuplicate != 1 {
				t.Fatalf("a single account changed its legacy tag: %+v", single)
			}

			first := ApplyDiff("accounts", nil, parsed)
			if len(first.New) != 2 || first.SkippedDuplicate != 1 {
				t.Fatalf("distinct accounts were discarded: New=%d SkippedDuplicate=%d, want 2 and 1", len(first.New), first.SkippedDuplicate)
			}
			tags := make(map[string]string)
			var current []string
			for _, member := range first.New {
				var outbound struct {
					Username string `json:"username"`
				}
				if err := json.Unmarshal(member.Out.Outbound, &outbound); err != nil {
					t.Fatal(err)
				}
				tags[outbound.Username] = member.Tag
				current = append(current, member.Tag)
			}
			if tags["alice"] == "" || tags["bob"] == "" || tags["alice"] == tags["bob"] {
				t.Fatalf("both accounts must survive with distinct tags: %v", tags)
			}

			preview, err := (&Service{}).previewBody([]byte(strings.Join(links, "\n")), "text/plain")
			if err != nil {
				t.Fatal(err)
			}
			if len(preview) != 2 {
				t.Fatalf("preview discarded a distinct account: got %d members, want 2", len(preview))
			}
			previewKeys := make(map[string]bool)
			for _, member := range preview {
				previewKeys[member.Key] = true
			}
			for _, tag := range current {
				if !previewKeys[tag[len(tag)-8:]] {
					t.Errorf("preview exclusion key does not match refresh tag %q", tag)
				}
			}

			// Reordering a feed must not replace either account or change its tag.
			next := ApplyDiff("accounts", current, []vlink.ParsedOutbound{parsed[1], parsed[0], parsed[2]})
			if len(next.New) != 0 || len(next.Orphan) != 0 || len(next.Existing) != 2 || next.SkippedDuplicate != 1 {
				t.Fatalf("unchanged accounts did not survive refresh: New=%d Existing=%d Orphan=%d SkippedDuplicate=%d", len(next.New), len(next.Existing), len(next.Orphan), next.SkippedDuplicate)
			}
			for _, member := range next.Existing {
				var outbound struct {
					Username string `json:"username"`
				}
				if err := json.Unmarshal(member.Out.Outbound, &outbound); err != nil {
					t.Fatal(err)
				}
				if member.Tag != tags[outbound.Username] {
					t.Errorf("tag changed for account %q", outbound.Username)
				}
			}
		})
	}
}
