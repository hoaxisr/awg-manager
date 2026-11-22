//go:build linux

package telemt

import "github.com/hoaxisr/awg-manager/internal/sys/iptables"

var firewallRun iptablesRunner = iptables.Run
