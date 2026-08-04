//go:build !linux

package procport

import "fmt"

func LookupListener(host string, port int, proto Proto) (ListenerInfo, error) {
	return ListenerInfo{Host: host, Port: port, Proto: string(proto)}, fmt.Errorf("lookup listener: linux only")
}

func KillListener(host string, port int, proto Proto) (ListenerInfo, error) {
	return ListenerInfo{}, fmt.Errorf("kill listener: linux only")
}
