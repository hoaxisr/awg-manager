//go:build !linux

package wdtt

import "context"

func applyRawServerPolicyMark(context.Context, string) error { return nil }

func removeRawServerPolicyMark(context.Context) {}

func rawServerPolicyMarkPresent(context.Context, string) bool { return true }
