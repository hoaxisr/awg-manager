package localdeps

import (
	"context"
	"errors"
	"fmt"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
)

func (l *Local) AddSingboxDNSRewrite(ctx context.Context, in mcpsrv.SingboxDNSRewriteInput) (mcpsrv.SingboxDNSRewriteResult, error) {
	if l.c.DNSRewrites == nil {
		return mcpsrv.SingboxDNSRewriteResult{}, errUnavailable("DNS Rewrite")
	}
	in, err := mcpsrv.NormalizeSingboxDNSRewrite(in)
	if err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, err
	}
	err = l.c.DNSRewrites.AddContext(ctx, dnsrewrite.DNSRewrite{Pattern: in.Pattern, IPs: in.IPs})
	return l.dnsRewriteResult(ctx, "dns-rewrite-add", err)
}

func (l *Local) UpdateSingboxDNSRewrite(ctx context.Context, index int, revision string, in mcpsrv.SingboxDNSRewriteInput) (mcpsrv.SingboxDNSRewriteResult, error) {
	if l.c.DNSRewrites == nil {
		return mcpsrv.SingboxDNSRewriteResult{}, errUnavailable("DNS Rewrite")
	}
	in, err := mcpsrv.NormalizeSingboxDNSRewrite(in)
	if err != nil {
		return mcpsrv.SingboxDNSRewriteResult{}, err
	}
	err = l.c.DNSRewrites.UpdateChecked(ctx, index, dnsrewrite.DNSRewrite{Pattern: in.Pattern, IPs: in.IPs}, revision)
	return l.dnsRewriteResult(ctx, "dns-rewrite-update", err)
}

func (l *Local) DeleteSingboxDNSRewrite(ctx context.Context, index int, revision string) (mcpsrv.SingboxDNSRewriteResult, error) {
	if l.c.DNSRewrites == nil {
		return mcpsrv.SingboxDNSRewriteResult{}, errUnavailable("DNS Rewrite")
	}
	err := l.c.DNSRewrites.DeleteChecked(ctx, index, revision)
	return l.dnsRewriteResult(ctx, "dns-rewrite-delete", err)
}

func (l *Local) dnsRewriteResult(ctx context.Context, action string, err error) (mcpsrv.SingboxDNSRewriteResult, error) {
	if err != nil {
		switch {
		case errors.Is(err, dnsrewrite.ErrConflict):
			return mcpsrv.SingboxDNSRewriteResult{}, fmt.Errorf("DNS Rewrite changed or shifted; nothing was changed by this call. Read list_singbox_dns_rewrites again and use its revision")
		case errors.Is(err, dnsrewrite.ErrManaged):
			return mcpsrv.SingboxDNSRewriteResult{}, fmt.Errorf("this DNS Rewrite is managed by a preset; nothing was changed. Manage the preset in the web interface")
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return mcpsrv.SingboxDNSRewriteResult{}, err
		default:
			// A flush may fail AFTER the store was saved. Do not claim a
			// rollback, invite an automatic retry, or expose paths from errors.
			l.rewriteLog.Warn(action, "", "DNS Rewrite save failed (MCP); persisted state must be checked before retrying")
			return mcpsrv.SingboxDNSRewriteResult{}, fmt.Errorf("DNS Rewrite save failed; settings may already have changed. Read list_singbox_dns_rewrites and check the web interface before retrying")
		}
	}
	l.rewriteLog.Info(action, "", "DNS Rewrite saved (MCP)")
	items, readErr := l.ListSingboxDNSRewrites(ctx)
	if readErr != nil {
		l.rewriteLog.Warn(action, "", "DNS Rewrite saved (MCP), but read-back failed")
		return mcpsrv.SingboxDNSRewriteResult{Saved: true, StateKnown: false, Rewrites: []mcpsrv.SingboxDNSRewrite{}}, nil
	}
	return mcpsrv.SingboxDNSRewriteResult{Saved: true, StateKnown: true, Rewrites: items}, nil
}
