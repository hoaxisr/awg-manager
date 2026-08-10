//go:build !linux

package wdtt

import "context"

func applyEntwareNAT(_ context.Context, _, _, _, _ string) error { return nil }

func applyEntwareNATForServer(_ context.Context, _ ServerConfig, _, _, _ string) error { return nil }

func removeEntwareNAT(_ context.Context, _ string) {}

func removeEntwareNATForServer(_ context.Context, _ ServerConfig) {}
