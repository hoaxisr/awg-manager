package autolist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetbridge"
)

func TestDirectPacketResultDoesNotBlockJudgeOwnerOnInspect(t *testing.T) {
	inspectStarted := make(chan struct{})
	unblockInspect := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(unblockInspect) }) }
	defer release()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		startOnce.Do(func() { close(inspectStarted) })
		<-unblockInspect
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"input":"blocked.example","final":"direct","matchedRule":-1}}`))
	}))
	defer srv.Close()

	eng := NewEngine(srv.URL, "")
	if err := eng.SetConfig(Config{PacketEnabled: true}); err != nil {
		t.Fatal(err)
	}
	flow := packet.Flow{
		Src: netip.MustParseAddr("192.168.1.10"), Dst: netip.MustParseAddr("203.0.113.7"),
		SrcPort: 51000, DstPort: 443, Protocol: packet.ProtocolTCP,
	}
	eng.obs.routes.Observe(flow, true, time.Now())
	failure := judge.Failure{Host: "blocked.example", Why: judge.WhyReset, Listed: true}
	result := packetbridge.Result{Kind: packet.RESET, Flow: flow, Failure: &failure}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.packetCorrelationWorker(ctx)
	returned := make(chan struct{})
	go func() {
		eng.processPacketResult(context.Background(), result)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("judge-owner blocked on inspect HTTP")
	}
	select {
	case <-inspectStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("correlation worker did not receive direct result")
	}
	release()
}
