package uplink

import "testing"

// OnStateChange заменяет 5-секундный опрос вкладки Multi-WAN: зовётся на
// смене видимого (фаза, провайдер, кандидат возврата и его счёт), но не на
// тиках счётчиков, которые меняются каждую пробу.
func TestControllerPublish_OnStateChangeOnlyForVisibleFields(t *testing.T) {
	c := New(nil, nil, nil)
	calls := 0
	c.OnStateChange = func(string) { calls++ }

	c.publish("p", State{Phase: "active", Provider: "ISP1"})
	if calls != 1 {
		t.Fatalf("first state: %d calls, want 1", calls)
	}
	c.publish("p", State{Phase: "active", Provider: "ISP1", Failures: 3, Generation: 9})
	if calls != 1 {
		t.Fatalf("counter tick fired: %d calls", calls)
	}
	c.publish("p", State{Phase: "active", Provider: "ISP1", Failures: 3, Candidate: "ISP2", RecoverySuccesses: 1})
	if calls != 2 {
		t.Fatalf("failback candidate: %d calls, want 2", calls)
	}
	c.publish("p", State{Phase: "active", Provider: "ISP1", Candidate: "ISP2", RecoverySuccesses: 2})
	if calls != 3 {
		t.Fatalf("failback progress: %d calls, want 3", calls)
	}
	c.publish("p", State{Phase: "active", Provider: "ISP1", Candidate: "ISP2", RecoverySuccesses: 2, LastError: "dial 1.1.1.1:40001: refused"})
	if calls != 4 {
		t.Fatalf("error appeared: %d calls, want 4", calls)
	}
	c.publish("p", State{Phase: "active", Provider: "ISP1", Candidate: "ISP2", RecoverySuccesses: 2, LastError: "dial 1.1.1.1:40777: refused"})
	if calls != 4 {
		t.Fatalf("error text with new port fired: %d calls", calls)
	}
}
