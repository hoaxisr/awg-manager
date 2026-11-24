package main

import (
	"github.com/hoaxisr/awg-manager/internal/updater"
)

// usageFeatures собирает флаги для анонимной статистики установок
// (internal/updater/stats.go): что включено, без количеств и содержимого.
func (a *app) usageFeatures() updater.Features {
	var f updater.Features
	if st, err := a.settingsStore.Get(); err == nil {
		f.SingboxRouter = st.SingboxRouter.Enabled
		f.SingboxMode = st.SingboxRouter.RoutingMode
	}
	if data := a.dnsRouteStore.GetCached(); data != nil {
		for _, l := range data.Lists {
			if l.Enabled && (l.Backend == "" || l.Backend == "ndms") {
				f.DNSRoute = true
				break
			}
		}
	}
	if routes, err := a.clientRouteService.List(); err == nil {
		for _, r := range routes {
			if r.Enabled {
				f.ClientRoute = true
				break
			}
		}
	}
	for _, in := range a.deviceProxySvc.GetSnapshot().Instances {
		if in.Enabled {
			f.DeviceProxy = true
			break
		}
	}
	return f
}
