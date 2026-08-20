// Package osdetect provides Keenetic OS version detection from cached NDMS info.
package osdetect

import (
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// Version represents Keenetic OS version.
type Version string

const (
	Version4x Version = "4.x"
	Version5  Version = "5.0+"
)

// parsedVersion holds the parsed major.minor.patch from the release string.
type parsedVersion struct {
	major, minor, patch int
	valid               bool
}

// parseRelease parses a version string like "5.1.3" into components.
func parseRelease(release string) parsedVersion {
	if release == "" {
		return parsedVersion{}
	}
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return parsedVersion{}
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return parsedVersion{}
	}
	// Minor may contain non-numeric suffix (e.g. "1-alpha3"), take only digits
	minorStr := parts[1]
	for i, c := range minorStr {
		if c < '0' || c > '9' {
			minorStr = minorStr[:i]
			break
		}
	}
	minor, err := strconv.Atoi(minorStr)
	if err != nil {
		return parsedVersion{major: major, valid: true}
	}
	var patch int
	if len(parts) >= 3 {
		patchStr := parts[2]
		for i, c := range patchStr {
			if c < '0' || c > '9' {
				patchStr = patchStr[:i]
				break
			}
		}
		patch, _ = strconv.Atoi(patchStr)
	}
	return parsedVersion{major: major, minor: minor, patch: patch, valid: true}
}

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

// Is5 returns true if running on Keenetic OS 5.0+.
func Is5() bool {
	return Get() == Version5
}

// AtLeast returns true if the firmware version is >= major.minor.
// Returns false if version info is not available.
func AtLeast(major, minor int) bool {
	info := ndmsinfo.Get()
	if info == nil {
		return false
	}
	v := parseRelease(info.Release)
	if !v.valid {
		return false
	}
	if v.major != major {
		return v.major > major
	}
	return v.minor >= minor
}

// opkgTunMinMajor — интерфейсы OpkgTun появились только в KeeneticOS 5.x.
// На 4.x NDMS отвечает на их создание HTTP 200 с вложенной ошибкой, интерфейс
// не появляется, и провижининг tun-режимов падает шагом позже (issue #768).
const opkgTunMinMajor = 5

// SupportsOpkgTunRelease сообщает, умеет ли прошивка интерфейсы OpkgTun.
//
// Нераспознанная версия трактуется как поддерживаемая: кэш ndmsinfo может быть
// ещё не прогрет, а ложная блокировка на живом OS5-роутере хуже, чем внятная
// ошибка от NDMS на редком OS4.
func SupportsOpkgTunRelease(release string) bool {
	v := parseRelease(strings.TrimSpace(release))
	if !v.valid {
		return true
	}
	return v.major >= opkgTunMinMajor
}

// SupportsOpkgTun — SupportsOpkgTunRelease для текущей прошивки.
func SupportsOpkgTun() bool { return SupportsOpkgTunRelease(ReleaseString()) }

// ReleaseString returns the raw release string from NDMS, or "" if unavailable.
func ReleaseString() string {
	info := ndmsinfo.Get()
	if info == nil {
		return ""
	}
	return info.Release
}
