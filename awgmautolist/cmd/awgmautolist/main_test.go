package main

import "testing"

func TestValidateLoopbackListen(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:2226", "[::1]:2226", "localhost:2226"} {
		if err := validateLoopbackListen(addr); err != nil {
			t.Fatalf("%s rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:2226", "192.168.1.1:2226", ":2226", "127.0.0.1"} {
		if err := validateLoopbackListen(addr); err == nil {
			t.Fatalf("%s unexpectedly accepted", addr)
		}
	}
}
