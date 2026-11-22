//go:build linux

package openflux

import "github.com/hoaxisr/awg-manager/internal/sys/iptables"

var firewallRun iptablesRunner = iptables.Run
