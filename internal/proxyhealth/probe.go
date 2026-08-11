package proxyhealth

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// RelayProbe performs a quick end-to-end HTTP check through a kernel interface.
type RelayProbe interface {
	ProbeInterface(ctx context.Context, iface string) bool
}

// LinkedTunnelResolver maps a proxy client id to its linked AWG tunnel iface.
type LinkedTunnelResolver interface {
	FreeTurnLinkedIface(clientID string) (iface string, ok bool)
}

// HTTPRelayProbe implements RelayProbe using the global connectivity check URL.
type HTTPRelayProbe struct {
	CheckURL func() string
}

func (p *HTTPRelayProbe) ProbeInterface(ctx context.Context, iface string) bool {
	if p == nil || iface == "" {
		return true
	}
	url := storage.DefaultConnectivityCheckURL
	if p.CheckURL != nil {
		if u := p.CheckURL(); u != "" {
			url = u
		}
	}
	res, err := httpprobe.ByInterface(ctx, iface, url, nil)
	return err == nil && httpprobe.SuccessCode(res.HTTPCode)
}
