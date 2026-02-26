// Package osdetect provides Keenetic OS version detection from cached NDMS info.
package osdetect

import (
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// Version represents Keenetic OS version.
type Version string

const (
	Version4x Version = "4.x"
	Version5  Version = "5.0+"
)

// Get returns the OS version from cached NDMS version info.
// Returns Version4x if info is not available (safe fallback).
func Get() Version {
	info := ndmsinfo.Get()
	if info == nil || info.Release == "" {
		return Version4x
	}
	if len(info.Release) > 0 && info.Release[0] >= '5' {
		return Version5
	}
	return Version4x
}

// Is4x returns true if running on Keenetic OS 4.x.
func Is4x() bool {
	return Get() == Version4x
}

// Is5 returns true if running on Keenetic OS 5.0+.
func Is5() bool {
	return Get() == Version5
}
