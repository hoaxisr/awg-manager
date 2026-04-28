// internal/singbox/awgoutbounds/awgoutboundstest/mock.go
package awgoutboundstest

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
)

// MockService is a test double satisfying awgoutbounds.Service. Used
// by deviceproxy, router, tunnel.Service, and api tests so downstream
// packages don't need to spin up the real catalog/store machinery.
type MockService struct {
	Tags        []awgoutbounds.TagInfo
	SyncCalls   int
	ReconCalls  int
	SyncErr     error
	ReconErr    error
	ListErr     error
}

func (m *MockService) SyncAWGOutbounds(ctx context.Context) error {
	m.SyncCalls++
	return m.SyncErr
}

func (m *MockService) Reconcile(ctx context.Context) error {
	m.ReconCalls++
	return m.ReconErr
}

func (m *MockService) ListTags(ctx context.Context) ([]awgoutbounds.TagInfo, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	return append([]awgoutbounds.TagInfo(nil), m.Tags...), nil
}

// Verify at compile-time that MockService implements the interface.
var _ awgoutbounds.Service = (*MockService)(nil)
