package router

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// firmwareRelease возвращает версию прошивки; подменяется в тестах через Deps.
func (s *ServiceImpl) firmwareRelease() string {
	if s.deps.FirmwareRelease != nil {
		return s.deps.FirmwareRelease()
	}
	return osdetect.ReleaseString()
}

// requireOpkgTunSupport — гейт tun-режимов (fakeip-tun, policy-tun): оба
// строятся на интерфейсе OpkgTun.
func (s *ServiceImpl) requireOpkgTunSupport() error {
	release := s.firmwareRelease()
	if osdetect.SupportsOpkgTunRelease(release) {
		return nil
	}
	return fmt.Errorf("режим требует интерфейс OpkgTun, которого нет в KeeneticOS %s — нужна версия 5.x или новее", release)
}
