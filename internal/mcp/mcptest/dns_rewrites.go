package mcptest

import (
	"context"
	"fmt"
	"slices"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
)

// DNS Rewrite writes mirror dnsrewrite.Service: append, replace whole,
// remove and shift positions. They are immediate and never touch Draft.
// Managed entries and stale revisions are refused under the same mutex.
func (f *Fake) rewriteListLocked() []mcpsrv.SingboxDNSRewrite {
	out := make([]mcpsrv.SingboxDNSRewrite, 0, len(f.DNSRewrites))
	stored := make([]dnsrewrite.DNSRewrite, 0, len(f.DNSRewrites))
	for _, r := range f.DNSRewrites {
		owner := ""
		if r.Managed {
			owner = "keendns"
		}
		stored = append(stored, dnsrewrite.DNSRewrite{Pattern: r.Pattern, IPs: r.IPs, Managed: owner})
	}
	revision := dnsrewrite.Revision(stored)
	for i, r := range f.DNSRewrites {
		r.Index = i
		r.IPs = slices.Clone(r.IPs)
		r.Revision = revision
		out = append(out, r)
	}
	return out
}

func (f *Fake) AddSingboxDNSRewrite(ctx context.Context, in mcpsrv.SingboxDNSRewriteInput) (mcpsrv.SingboxDNSRewriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, err
	}
	if f.Err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, f.Err
	}
	in, err := mcpsrv.NormalizeSingboxDNSRewrite(in)
	if err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, err
	}
	f.DNSRewrites = append(f.DNSRewrites, mcpsrv.SingboxDNSRewrite{Pattern: in.Pattern, IPs: slices.Clone(in.IPs)})
	return mcpsrv.SingboxDNSRewriteResult{Saved: true, StateKnown: true, Rewrites: f.rewriteListLocked()}, nil
}

func (f *Fake) UpdateSingboxDNSRewrite(ctx context.Context, index int, revision string, in mcpsrv.SingboxDNSRewriteInput) (mcpsrv.SingboxDNSRewriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkRewriteLocked(ctx, index, revision); err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, err
	}
	in, err := mcpsrv.NormalizeSingboxDNSRewrite(in)
	if err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, err
	}
	f.DNSRewrites[index] = mcpsrv.SingboxDNSRewrite{Pattern: in.Pattern, IPs: slices.Clone(in.IPs)}
	return mcpsrv.SingboxDNSRewriteResult{Saved: true, StateKnown: true, Rewrites: f.rewriteListLocked()}, nil
}

func (f *Fake) DeleteSingboxDNSRewrite(ctx context.Context, index int, revision string) (mcpsrv.SingboxDNSRewriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.checkRewriteLocked(ctx, index, revision); err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, err
	}
	f.DNSRewrites = slices.Delete(f.DNSRewrites, index, index+1)
	return mcpsrv.SingboxDNSRewriteResult{Saved: true, StateKnown: true, Rewrites: f.rewriteListLocked()}, nil
}

func (f *Fake) checkRewriteLocked(ctx context.Context, index int, revision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.Err != nil {
		return f.Err
	}
	if index < 0 || index >= len(f.DNSRewrites) {
		return fmt.Errorf("DNS Rewrite index out of range")
	}
	r := f.rewriteListLocked()[index]
	if revision == "" || r.Revision != revision {
		return dnsrewrite.ErrConflict
	}
	if r.Managed {
		return dnsrewrite.ErrManaged
	}
	return nil
}
