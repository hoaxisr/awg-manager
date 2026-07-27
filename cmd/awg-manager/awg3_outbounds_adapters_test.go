package main

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
)

// fakeAWGOutboundsSvc is a stand-in for awgoutbounds.Service exposing a
// fixed tag set; it lets the adapter tests run without a live catalog.
type fakeAWGOutboundsSvc struct {
	tags []awgoutbounds.TagInfo
}

func (f *fakeAWGOutboundsSvc) SyncAWGOutbounds(ctx context.Context) error { return nil }
func (f *fakeAWGOutboundsSvc) Reconcile(ctx context.Context) error        { return nil }
func (f *fakeAWGOutboundsSvc) ListTags(ctx context.Context) ([]awgoutbounds.TagInfo, error) {
	return f.tags, nil
}

// fakeAwg3Lister satisfies awg3TagLister with a fixed AWG3 tag set.
type fakeAwg3Lister struct {
	tags []awg3endpoint.TagInfo
}

func (f *fakeAwg3Lister) ListTags() []awg3endpoint.TagInfo { return f.tags }

// Step 1: the dropdown source (fed into api.NewAWGOutboundsHandler) must
// merge AWG3 tags into awgoutbounds tags with Kind=="awg3".
func TestAwg3MergedAWGOutbounds_ListTags(t *testing.T) {
	inner := &fakeAWGOutboundsSvc{tags: []awgoutbounds.TagInfo{
		{Tag: "awg-vpn0", Label: "My VPN", Kind: "managed", Iface: "nwg0"},
	}}
	awg3 := &fakeAwg3Lister{tags: []awg3endpoint.TagInfo{{Tag: "awg3-berlin", Kind: "awg3"}}}

	merged := &awg3MergedAWGOutbounds{inner: inner, awg3: awg3}
	got, err := merged.ListTags(context.Background())
	if err != nil {
		t.Fatalf("ListTags error: %v", err)
	}

	var found *awgoutbounds.TagInfo
	for i := range got {
		if got[i].Tag == "awg3-berlin" {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("AWG3 tag awg3-berlin missing from merged catalog: %+v", got)
	}
	if found.Kind != "awg3" {
		t.Errorf("AWG3 tag Kind = %q, want %q", found.Kind, "awg3")
	}
	if found.Label != "awg3-berlin" {
		t.Errorf("AWG3 tag Label = %q, want tag as label", found.Label)
	}
	// Inner tags must be preserved.
	if got[0].Tag != "awg-vpn0" {
		t.Errorf("inner tag lost, got[0] = %+v", got[0])
	}
}

// Step 2: the router validator adapter must surface AWG3 tags so rules
// referencing them don't produce "dangling outbound" issues.
func TestRouterAWGTagAdapter_IncludesAwg3(t *testing.T) {
	inner := &fakeAWGOutboundsSvc{tags: []awgoutbounds.TagInfo{{Tag: "awg-vpn0"}}}
	awg3 := &fakeAwg3Lister{tags: []awg3endpoint.TagInfo{{Tag: "awg3-berlin", Kind: "awg3"}}}

	adapter := &routerAWGTagAdapter{src: inner, awg3: awg3}
	got, err := adapter.ListTags(context.Background())
	if err != nil {
		t.Fatalf("ListTags error: %v", err)
	}
	tags := make([]string, 0, len(got))
	for _, g := range got {
		tags = append(tags, g.Tag)
	}
	if !containsTag(tags, "awg3-berlin") {
		t.Fatalf("AWG3 tag missing from router validator: %+v", got)
	}
}

// Step 3: deviceproxy must NOT see AWG3 tags — its adapter wraps only the
// awgoutbounds catalog (AWG3 endpoints have no kernel iface for bind).
func TestDeviceproxyAWGOutboundsAdapter_ExcludesAwg3(t *testing.T) {
	inner := &fakeAWGOutboundsSvc{tags: []awgoutbounds.TagInfo{{Tag: "awg-vpn0", Label: "My VPN"}}}

	adapter := &deviceproxyAWGOutboundsAdapter{src: inner}
	got, err := adapter.ListTags(context.Background())
	if err != nil {
		t.Fatalf("ListTags error: %v", err)
	}
	for _, tg := range got {
		if tg.Tag == "awg3-berlin" {
			t.Fatalf("AWG3 tag leaked into deviceproxy catalog: %+v", got)
		}
	}
}

// Step 4: the delaychecker lister must include AWG3 tags so auto periodic
// delay checks probe them.
func TestSingboxAndSubLister_ListSubActiveTags_IncludesAwg3(t *testing.T) {
	awg3 := &fakeAwg3Lister{tags: []awg3endpoint.TagInfo{{Tag: "awg3-berlin", Kind: "awg3"}}}
	// sub is nil here — ListSubActiveTags must be nil-safe and still return AWG3 tags.
	lister := &singboxAndSubLister{sub: nil, awg3: awg3}
	got := lister.ListSubActiveTags()
	if !containsTag(got, "awg3-berlin") {
		t.Fatalf("AWG3 tag missing from delaychecker lister: %v", got)
	}
}

func containsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}
