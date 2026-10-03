package adaptiverouting

import (
	"fmt"
	"strings"
)

type ReferenceChecker struct {
	store *Store
}

func NewReferenceChecker(store *Store) *ReferenceChecker {
	return &ReferenceChecker{store: store}
}

// InUseChecker returns a callback matching mihomonative.InUseChecker
func (rc *ReferenceChecker) InUseChecker() func(kind string, id string, name string) (bool, string) {
	return func(kind string, id string, name string) (bool, string) {
		return rc.CheckResourceInUse(kind, id, name)
	}
}

func (rc *ReferenceChecker) CheckResourceInUse(kind string, id string, name string) (bool, string) {
	if rc.store == nil {
		return false, ""
	}
	settings := rc.store.GetSettings()
	if !settings.Enabled {
		// Even if not currently active, protect resources configured as primary egress
	}

	targets := append([]EgressRef{settings.PrimaryEgress}, settings.FallbackEgresses...)
	for _, target := range targets {
		if target.ResourceID == "" {
			continue
		}
		match := false
		if id != "" && (target.ResourceID == id || strings.EqualFold(target.ResourceID, id)) {
			match = true
		}
		if name != "" && strings.EqualFold(target.ResourceID, name) {
			match = true
		}

		if match {
			switch target.Kind {
			case EgressKindMihomoGroup:
				if kind == "group" || kind == "name" {
					return true, fmt.Sprintf("используется в адаптивной маршрутизации Susanin как выход (%s)", target.ResourceID)
				}
			case EgressKindMihomoProxy:
				if kind == "proxy" || kind == "name" {
					return true, fmt.Sprintf("используется в адаптивной маршрутизации Susanin как выход (%s)", target.ResourceID)
				}
			case EgressKindMihomoSubscription:
				if kind == "subscription" || kind == "name" {
					return true, fmt.Sprintf("используется в адаптивной маршрутизации Susanin как выход (%s)", target.ResourceID)
				}
			case EgressKindKernelTunnel:
				if kind == "tunnel" || kind == "name" {
					return true, fmt.Sprintf("используется в адаптивной маршрутизации Susanin как выход (%s)", target.ResourceID)
				}
			}
		}
	}

	return false, ""
}
