// Package cleanup orchestrates complete removal of all awg-manager resources.
// Called by main.go --cleanup (opkg remove).
package cleanup

import (
	"context"
	"fmt"
	"os"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TunnelDeleter deletes individual tunnels.
type TunnelDeleter interface {
	Delete(ctx context.Context, tunnelID string) error
}

// TunnelLister lists stored tunnels.
type TunnelLister interface {
	List() ([]storage.AWGTunnel, error)
}

// DnsRouteCleaner removes all DNS route NDMS objects.
type DnsRouteCleaner interface {
	CleanupAll(ctx context.Context) error
}

// ManagedServerCleaner removes the managed WG server.
type ManagedServerCleaner interface {
	DeleteIfExists(ctx context.Context) error
}

// PolicyCleaner removes all managed access policies.
type PolicyCleaner interface {
	CleanupAll(ctx context.Context) error
}

// ClientRouteCleaner removes all client VPN routing rules.
type ClientRouteCleaner interface {
	CleanupAll(ctx context.Context) error
}

// ConfigSaver persists NDMS configuration.
type ConfigSaver interface {
	Save(ctx context.Context) error
}

// Service orchestrates complete cleanup.
type Service struct {
	tunnelDeleter TunnelDeleter
	tunnelLister  TunnelLister
	dnsRoutes     DnsRouteCleaner
	managed       ManagedServerCleaner
	policies      PolicyCleaner
	clientRoutes  ClientRouteCleaner
	saver         ConfigSaver
}

// New creates a CleanupService. Any dependency can be nil — it will be skipped.
func New(
	deleter TunnelDeleter,
	lister TunnelLister,
	dnsRoutes DnsRouteCleaner,
	managed ManagedServerCleaner,
	policies PolicyCleaner,
	clientRoutes ClientRouteCleaner,
	saver ConfigSaver,
) *Service {
	return &Service{
		tunnelDeleter: deleter,
		tunnelLister:  lister,
		dnsRoutes:     dnsRoutes,
		managed:       managed,
		policies:      policies,
		clientRoutes:  clientRoutes,
		saver:         saver,
	}
}

// CleanupAll removes all resources created by awg-manager.
// Order: tunnels first (hooks notify dependent services), then auxiliary.
// Errors are logged but do not stop cleanup — best-effort removal.
func (s *Service) CleanupAll(ctx context.Context) error {
	// 1. Delete all tunnels
	if s.tunnelLister != nil && s.tunnelDeleter != nil {
		tunnels, err := s.tunnelLister.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Failed to list tunnels: %v\n", err)
		} else {
			var deleted, failed int
			for _, t := range tunnels {
				fmt.Printf("  Deleting tunnel %s (%s)...\n", t.ID, t.Name)
				if err := s.tunnelDeleter.Delete(ctx, t.ID); err != nil {
					fmt.Fprintf(os.Stderr, "    Error: %v\n", err)
					failed++
				} else {
					fmt.Printf("    Deleted\n")
					deleted++
				}
			}
			if len(tunnels) > 0 {
				fmt.Printf("  Tunnels: %d deleted, %d failed\n", deleted, failed)
			}
		}
	}

	// 1.5. Remove client VPN routing rules
	if s.clientRoutes != nil {
		fmt.Println("  Cleaning client VPN routes...")
		if err := s.clientRoutes.CleanupAll(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: client routes: %v\n", err)
		}
	}

	// 2. Remove DNS routes (AWG_* object-groups in NDMS)
	if s.dnsRoutes != nil {
		fmt.Println("  Cleaning DNS routes...")
		if err := s.dnsRoutes.CleanupAll(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: DNS routes: %v\n", err)
		}
	}

	// 3. Remove managed server (if exists)
	if s.managed != nil {
		if err := s.managed.DeleteIfExists(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: managed server: %v\n", err)
		}
	}

	// 4. Remove access policies created by awg-manager
	if s.policies != nil {
		fmt.Println("  Cleaning access policies...")
		if err := s.policies.CleanupAll(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "    Warning: access policies: %v\n", err)
		}
	}

	// 5. Persist NDMS configuration
	if s.saver != nil {
		_ = s.saver.Save(ctx)
	}

	return nil
}
