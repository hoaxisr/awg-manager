//go:build !linux

package events

import "errors"

// Демон работает только на роутере (linux); заглушка — чтобы пакет
// собирался на машине разработчика.

func (r *SpoolReader) Start() error { return errors.New("spool: unsupported platform") }

func (r *SpoolReader) Stop() {}
