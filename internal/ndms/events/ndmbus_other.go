//go:build !linux

package events

import "errors"

// Демон работает только на роутере (linux); заглушка — чтобы пакет
// собирался на машине разработчика.

func (r *BusReader) Start() error { return errors.New("ndm bus: unsupported platform") }

func (r *BusReader) Stop() {}
