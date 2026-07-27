package pingcheck

import (
	testingsvc "github.com/hoaxisr/awg-manager/internal/testing"
)

// reasonCheckDisabled duplicates the literal returned by
// testing.Service.CheckConnectivity for method "disabled"
// (internal/testing/connectivity.go:46). It is a plain string there, not a
// constant, so this copy is the coupling point — keep them in sync.
const reasonCheckDisabled = "check disabled"

// LatencyFromConnectivity converts a connectivity-check result into the latency
// value stored in the ping log and a note explaining an absent measurement.
//
// Only the "http" and "ping" methods measure time (connectivity.go:101 and
// :133); "handshake" and "disabled" report success with no latency at all, so
// an absent value is normal rather than a failure. Collapsing every case into
// LatencyNotAvailable without a note is what made the UI render a successful
// check as "-1 ms" with a red bar (issue #629).
func LatencyFromConnectivity(res *testingsvc.ConnectivityResult, err error) (int, string) {
	switch {
	case err != nil:
		return LatencyNotAvailable, "проверка связности не выполнилась: " + err.Error()
	case res == nil:
		return LatencyNotAvailable, "проверка связности не вернула результат"
	case !res.Connected:
		note := "проверка связности не прошла"
		if res.Reason != "" {
			note += ": " + res.Reason
		}
		return LatencyNotAvailable, note
	case res.Latency == nil:
		if res.Reason == reasonCheckDisabled {
			return LatencyNotAvailable, "проверка связности выключена — время отклика не измеряется"
		}
		return LatencyNotAvailable, "метод проверки не измеряет время отклика — выберите «ping» или «http», чтобы видеть числа"
	}
	return *res.Latency, ""
}
