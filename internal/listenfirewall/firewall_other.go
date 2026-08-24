//go:build !linux

package listenfirewall

import "context"

func Apply(_ context.Context, _ int, _ string) error { return nil }

func Remove(_ context.Context, _ int, _ string) {}

func Present(_ context.Context, _ int, _ string) bool { return false }

func Reconcile(_ context.Context, _ []PortSpec) {}

func ListManaged(_ context.Context) ([]PortSpec, error) { return nil, nil }
