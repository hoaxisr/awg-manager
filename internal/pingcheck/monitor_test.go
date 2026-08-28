package pingcheck

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// slowWGClient simulates a WG client that never returns a recent handshake.
type slowWGClient struct{}

func (c *slowWGClient) Show(ctx context.Context, iface string) (*wg.ShowResult, error) {
	return &wg.ShowResult{HasPeer: true}, nil // no recent handshake
}

// TestWaitHandshake_InterruptedByStopCh verifies that waitHandshake exits
// immediately when stopCh is closed, rather than blocking for the full
// 30-second handshake timeout. This prevents HTTP handlers from hanging
// when StopMonitoring is called during an active link toggle.
func TestWaitHandshake_InterruptedByStopCh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Service{
		wg:  &slowWGClient{},
		ctx: ctx,
	}

	stopCh := make(chan struct{})

	// Close stopCh after a short delay to simulate StopMonitoring.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(stopCh)
	}()

	start := time.Now()
	result := s.waitHandshake("fake0", stopCh)
	elapsed := time.Since(start)

	if result {
		t.Error("waitHandshake should return false when interrupted by stopCh")
	}
	if elapsed > 2*time.Second {
		t.Errorf("waitHandshake took %v — should have exited quickly via stopCh, not waited for 30s deadline", elapsed)
	}
}

// TestWaitHandshake_DeadlineWithoutStop verifies that waitHandshake respects
// the configured handshake deadline when stopCh is NOT closed (normal path).
func TestWaitHandshake_DeadlineWithoutStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const fastTimeout = 40 * time.Millisecond
	s := &Service{
		wg:               &slowWGClient{},
		ctx:              ctx,
		handshakeTimeout: fastTimeout,
	}

	stopCh := make(chan struct{}) // never closed

	start := time.Now()
	result := s.waitHandshake("fake0", stopCh)
	elapsed := time.Since(start)

	if result {
		t.Error("waitHandshake should return false when deadline expires")
	}
	if elapsed < fastTimeout/2 {
		t.Errorf("waitHandshake returned too quickly (%v) — deadline should be about %v", elapsed, fastTimeout)
	}
	if elapsed > time.Second {
		t.Errorf("waitHandshake took too long (%v) for configured timeout %v", elapsed, fastTimeout)
	}
}

// spyWGClient считает обращения к wg. waitHandshake — последний шаг лечения:
// добраться до него можно только через `ip link set down/up`, поэтому ноль
// обращений и есть наблюдаемый признак «команд управления не было».
type spyWGClient struct {
	calls int
}

func (c *spyWGClient) Show(_ context.Context, _ string) (*wg.ShowResult, error) {
	c.calls++
	return &wg.ShowResult{HasPeer: true}, nil
}

// alwaysFailDoer роняет HTTP-проверку мгновенно и без сети.
type alwaysFailDoer struct{}

func (alwaysFailDoer) Do(_ context.Context, _ httpclient.CallConfig) (*httpclient.Result, error) {
	return nil, errors.New("no route")
}

// Зеркальную запись прокси-выхода мы измеряем, но не лечим: три провала
// подряд не должны выливаться ни в одну команду управления интерфейсом —
// tun принадлежит прокси-рантайму, у него свой цикл реконсиляции.
func TestSensorTick_WdttRawNeverTouchesInterface(t *testing.T) {
	orig := httpprobe.Client
	defer func() { httpprobe.Client = orig }()
	httpprobe.Client = alwaysFailDoer{}

	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Save(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        "wdtt-raw",
		RawKernelIface: "opkgtun18",
		PingCheck: &storage.TunnelPingCheck{
			Enabled: true, Method: "http", Interval: 1, FailThreshold: 3,
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lb := NewLogBuffer()
	defer lb.Stop()
	spy := &spyWGClient{}
	// Порог рукопожатия выше частоты опроса (handshakePollFreq): если гард
	// пропадёт, лечение реально дойдёт до wg.Show, и spy это увидит.
	s := &Service{tunnels: store, wg: spy, logBuffer: lb, ctx: ctx, handshakeTimeout: 3 * time.Second}

	config := s.getCheckConfig("wdttraw-de")
	if config == nil || config.FailThreshold != 3 {
		t.Fatalf("config = %+v, ожидали порог 3", config)
	}
	m := &tunnelMonitor{tunnelID: "wdttraw-de", tunnelName: "Германия"}
	for i := 0; i < 3; i++ {
		s.sensorTick(m, config)
	}

	// Улика «три провала действительно случились» берётся из журнала, а не из
	// failCount: лечение обнуляет счётчик, и проверка по нему путала бы
	// «проверки не падали» с «лечение отработало и сбросило счёт».
	fails, transitions := 0, ""
	for _, e := range lb.GetAll() {
		if e.StateChange != "" {
			transitions = e.StateChange
			continue // запись самого лечения, а не сенсора
		}
		if !e.Success {
			fails++
		}
	}
	if fails != config.FailThreshold {
		t.Fatalf("провалов в журнале = %d, ожидали %d: точка лечения не достигнута", fails, config.FailThreshold)
	}
	if spy.calls != 0 {
		t.Fatalf("wg.Show вызван %d раз — значит link toggle отработал", spy.calls)
	}
	if m.restartCount != 0 {
		t.Fatalf("restartCount = %d, ожидали 0: восстановление не наше", m.restartCount)
	}
	if transitions != "" {
		t.Fatalf("в журнале есть переход %q — восстановление отработало", transitions)
	}
}
