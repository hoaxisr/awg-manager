// Package engine coordinates AF_PACKET evidence, active checks and user actions.
package engine

import (
	"context"
	"log"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/capture"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

type CoverageOutcome struct {
	Evidence model.CoverageEvidence
	Revision string
}

type Coverage interface {
	Check(context.Context, capture.Suspect, config.Config) (CoverageOutcome, error)
}

// WriteResult is the writer outcome. It is the same type as awgm.WriteResult:
// the concrete writer lives in internal/awgm and cannot import this package
// (cycle), so the result type is defined there and aliased here.
type WriteResult = awgm.WriteResult

type Writer interface {
	Add(context.Context, model.Target, string) (WriteResult, error)
	Remove(context.Context, model.Target, string) (WriteResult, error)
}

// The concrete AWGM writer must satisfy the interface without an adapter.
var _ Writer = (*awgm.Writer)(nil)

type Probes interface {
	Check(context.Context, model.Target, probe.Path) probe.Result
	DirectAndWAN(context.Context, model.Target, string, probe.Path) probe.DirectAndWANResult
	CheckWAN(context.Context, string, probe.Path) probe.Result
}

type Routes interface {
	Resolve(context.Context) (awgm.DirectPath, error)
}

type Capture interface {
	Start(context.Context) error
	Pause() error
	Stop() error
	Status() capture.CaptureStatus
}

type CaptureFactory func(udpAll bool, emit func(capture.Suspect)) Capture

type Dependencies struct {
	Config         *config.Store
	State          *store.Store
	Coverage       Coverage
	Probes         Probes
	Routes         Routes
	Writer         Writer
	CaptureFactory CaptureFactory
	Logger         *log.Logger
	DefaultTunnel  func(context.Context) (awgm.Tunnel, error)
}
