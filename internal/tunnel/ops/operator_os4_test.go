package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// TestNewOperatorOS4_IPRunDefaultsToExec пинует дефолт шва ipRun: удаление
// присваивания `ipRun: exec.Run` в NewOperatorOS4 оставляет nil-функцию, и
// любой Start/Stop/Reconcile на реальном OS4 паникует на первом вызове ip.
func TestNewOperatorOS4_IPRunDefaultsToExec(t *testing.T) {
	op := NewOperatorOS4(nil, nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	if op.ipRun == nil {
		t.Error("ipRun должен быть установлен дефолтом (exec.Run)")
	}
	if op.clientRouteOps == nil || op.clientRouteOps.run == nil {
		t.Error("clientRouteOps.run должен быть закрытием над ipRun")
	}
}

// TestOperatorOS4_Create_NoOp verifies Create is a no-op on OS4.
func TestOperatorOS4_Create_NoOp(t *testing.T) {
	backendMock := &MockBackend{}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, nil, wgClient, backendMock, fw)

	cfg := tunnel.Config{
		ID:   "awg0",
		Name: "Test Tunnel",
	}

	err := op.Create(context.Background(), cfg)

	if err != nil {
		t.Fatalf("Create() should be no-op, got error: %v", err)
	}
	// No backend calls should happen
	if len(backendMock.StartCalls) != 0 {
		t.Errorf("Backend should not be started on Create")
	}
}

// Порядок Start на OS4: адрес → wg.SetConf → up → mtu → txqueuelen.
// Команды зафиксированы литералами, а не рендером: `/32` в адресе — прод-факт
// (configureIP игнорирует cfg.AddressPrefix, здесь он намеренно 26), а up, mtu
// и txqueuelen на OS4 — три отдельные команды, а не одна.
func TestOperatorOS4_Start_VerifySequence(t *testing.T) {
	backendMock := &MockBackend{}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, nil, wgClient, backendMock, fw)
	rec := &ipRunRecorder{}
	op.ipRun = rec.run

	cfg := tunnel.Config{
		ID:            "awgm5",
		Name:          "Test",
		Address:       "10.9.7.2",
		AddressPrefix: 26,
		MTU:           1342,
		ConfPath:      t.TempDir() + "/awgm5.conf",
	}

	if err := op.Start(context.Background(), cfg); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	want := []string{
		"/opt/sbin/ip address add dev awgm5 10.9.7.2/32",
		"/opt/sbin/ip link set up dev awgm5",
		"/opt/sbin/ip link set dev awgm5 mtu 1342",
		"/opt/sbin/ip link set dev awgm5 txqueuelen 1000",
	}
	if strings.Join(rec.Calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("ip-команды Start:\nполучено:\n%s\nожидалось:\n%s",
			strings.Join(rec.Calls, "\n"), strings.Join(want, "\n"))
	}

	if len(backendMock.StartCalls) != 1 || backendMock.StartCalls[0] != "awgm5" {
		t.Errorf("Backend.Start = %v, want [awgm5]", backendMock.StartCalls)
	}
	if len(wgClient.SetConfCalls) != 1 || wgClient.SetConfCalls[0].Iface != "awgm5" {
		t.Errorf("WG.SetConf = %v, want один вызов на awgm5", wgClient.SetConfCalls)
	}
	if len(fw.AddCalls) != 1 || fw.AddCalls[0] != "awgm5" {
		t.Errorf("Firewall.AddRules = %v, want [awgm5]", fw.AddCalls)
	}
}

func TestOperatorOS4_Start_BackendFails(t *testing.T) {
	backendMock := &MockBackend{startError: errors.New("process failed")}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, nil, wgClient, backendMock, fw)
	op.ipRun = mockIPRun

	cfg := tunnel.Config{
		ID:       "awg0",
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg0.conf",
	}

	err := op.Start(context.Background(), cfg)

	if err == nil {
		t.Fatal("Start() should fail when backend fails")
	}
	// WG config should not be applied if backend fails
	if len(wgClient.SetConfCalls) != 0 {
		t.Errorf("WG.SetConf should not be called on backend failure")
	}
}

// Отказ wg.SetConf обязан откатить всё, что Start успел сделать: остановить
// процесс и снести интерфейс. `link del` — БЕЗ `dev` (deleteInterface).
func TestOperatorOS4_Start_WGFails_Rollback(t *testing.T) {
	backendMock := &MockBackend{}
	wgClient := &MockWGClient{setConfError: errors.New("setconf: boom")}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, nil, wgClient, backendMock, fw)
	rec := &ipRunRecorder{}
	op.ipRun = rec.run

	cfg := tunnel.Config{
		ID:       "awgm5",
		Address:  "10.9.7.2",
		MTU:      1342,
		ConfPath: t.TempDir() + "/awgm5.conf",
	}

	err := op.Start(context.Background(), cfg)
	if err == nil {
		t.Fatal("Start() должен упасть при отказе wg.SetConf")
	}
	if !strings.Contains(err.Error(), "setconf: boom") {
		t.Errorf("ошибка не от wg.SetConf: %v", err)
	}

	if len(backendMock.StopCalls) != 1 || backendMock.StopCalls[0] != "awgm5" {
		t.Errorf("Backend.Stop = %v, want [awgm5]", backendMock.StopCalls)
	}
	want := []string{
		"/opt/sbin/ip address add dev awgm5 10.9.7.2/32",
		"/opt/sbin/ip link set down dev awgm5",
		"/opt/sbin/ip link del awgm5",
	}
	if strings.Join(rec.Calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("ip-команды отката:\nполучено:\n%s\nожидалось:\n%s",
			strings.Join(rec.Calls, "\n"), strings.Join(want, "\n"))
	}
	if len(fw.AddCalls) != 0 {
		t.Errorf("правила файрвола поставлены при провале Start: %v", fw.AddCalls)
	}
}

// Stop ждёт исчезновения интерфейса через `ip link show`. Отказ этой команды
// = интерфейса уже нет: ждать нечего и добивать `link del` нельзя.
func TestOperatorOS4_Stop_Success(t *testing.T) {
	backendMock := &MockBackend{running: true}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, nil, &MockWGClient{}, backendMock, fw)
	ip := &scriptedIPRun{linkShowErr: errors.New("device \"awg0\" does not exist")}
	op.ipRun = ip.run

	err := op.Stop(context.Background(), "awg0")

	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Verify firewall rules removed
	if len(fw.RemoveCalls) != 1 {
		t.Errorf("Firewall.RemoveRules not called")
	}

	// Verify backend stopped
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop not called")
	}
	if backendMock.StopCalls[0] != "awg0" {
		t.Errorf("Backend.Stop iface = %s, want awg0", backendMock.StopCalls[0])
	}

	if !hasCall(ip.Calls, "/opt/sbin/ip link show awg0") {
		t.Errorf("исчезновение интерфейса не проверялось:\n%s", strings.Join(ip.Calls, "\n"))
	}
	if hasCall(ip.Calls, "/opt/sbin/ip link del awg0") {
		t.Errorf("интерфейс добит, хотя его уже нет:\n%s", strings.Join(ip.Calls, "\n"))
	}
}

func TestOperatorOS4_Delete_SameAsStop(t *testing.T) {
	backendMock := &MockBackend{running: true}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, nil, &MockWGClient{}, backendMock, fw)
	ip := &scriptedIPRun{linkShowErr: errors.New("device \"awg0\" does not exist")}
	op.ipRun = ip.run

	err := op.Delete(context.Background(), &storage.AWGTunnel{ID: "awg0"})

	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// On OS4, Delete is the same as Stop
	if len(backendMock.StopCalls) != 1 {
		t.Errorf("Backend.Stop not called on Delete")
	}
	if !hasCall(ip.Calls, "/opt/sbin/ip link show awg0") {
		t.Errorf("Delete не прошёл путём Stop:\n%s", strings.Join(ip.Calls, "\n"))
	}
}

func TestOperatorOS4_ApplyConfig(t *testing.T) {
	wgClient := &MockWGClient{}
	op := NewOperatorOS4(nil, nil, wgClient, &MockBackend{}, &MockFirewall{})
	op.ipRun = mockIPRun

	err := op.ApplyConfig(context.Background(), "awg0", "/tmp/new.conf")

	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if len(wgClient.SetConfCalls) != 1 {
		t.Errorf("WG.SetConf not called")
	}
	// On OS4, interface name is tunnel ID
	if wgClient.SetConfCalls[0].Iface != "awg0" {
		t.Errorf("SetConf iface = %s, want awg0", wgClient.SetConfCalls[0].Iface)
	}
}

func TestOperatorOS4_UsesDirectTunnelID(t *testing.T) {
	// Verify OS4 uses tunnel ID directly as interface name
	// (unlike OS5 which converts awg0 -> opkgtun0)

	backendMock := &MockBackend{}
	wgClient := &MockWGClient{}
	fw := &MockFirewall{}

	op := NewOperatorOS4(nil, nil, wgClient, backendMock, fw)
	// linkShowErr — иначе ожидание в Stop крутится до таймаута в 5 с.
	op.ipRun = (&scriptedIPRun{linkShowErr: errors.New("device does not exist")}).run

	cfg := tunnel.Config{
		ID:       "awg1", // Different ID to verify
		Address:  "10.0.0.1",
		MTU:      1420,
		ConfPath: "/tmp/awg1.conf",
	}

	_ = op.Start(context.Background(), cfg)

	// Backend should use tunnel ID directly
	if len(backendMock.StartCalls) == 0 {
		t.Fatal("Backend.Start not called")
	}
	if backendMock.StartCalls[0] != "awg1" {
		t.Errorf("Backend.Start iface = %s, want awg1 (direct ID)", backendMock.StartCalls[0])
	}

	// ip is mocked, so Start succeeds fully; WG and Firewall calls do
	// happen here, but this test only checks the Stop behavior below.
	_ = op.Stop(context.Background(), "awg1")

	if len(backendMock.StopCalls) == 0 {
		t.Fatal("Backend.Stop not called")
	}
	if backendMock.StopCalls[0] != "awg1" {
		t.Errorf("Backend.Stop iface = %s, want awg1 (direct ID)", backendMock.StopCalls[0])
	}

	// Firewall should use tunnel ID directly (on Stop)
	if len(fw.RemoveCalls) == 0 {
		t.Fatal("Firewall.RemoveRules not called")
	}
	if fw.RemoveCalls[0] != "awg1" {
		t.Errorf("Firewall.RemoveRules iface = %s, want awg1 (direct ID)", fw.RemoveCalls[0])
	}
}

// === Endpoint route no-op tests (routing not managed on OS4) ===

func TestOperatorOS4_SetupEndpointRoute_NoOp(t *testing.T) {
	op := NewOperatorOS4(nil, nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	ip, err := op.SetupEndpointRoute(context.Background(), "awgm0", "1.2.3.4:51820", "", "")
	if err != nil {
		t.Fatalf("SetupEndpointRoute() error = %v", err)
	}
	if ip != "" {
		t.Errorf("SetupEndpointRoute() = %q, want empty (no-op on OS4)", ip)
	}
}

func TestOperatorOS4_CleanupEndpointRoute_NoOp(t *testing.T) {
	op := NewOperatorOS4(nil, nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	err := op.CleanupEndpointRoute(context.Background(), "awgm0")
	if err != nil {
		t.Errorf("CleanupEndpointRoute() error = %v", err)
	}
}

func TestOperatorOS4_GetTrackedEndpointIP_NoOp(t *testing.T) {
	op := NewOperatorOS4(nil, nil, &MockWGClient{}, &MockBackend{}, &MockFirewall{})

	got := op.GetTrackedEndpointIP("awgm0")
	if got != "" {
		t.Errorf("GetTrackedEndpointIP() = %q, want empty (no-op on OS4)", got)
	}
}
