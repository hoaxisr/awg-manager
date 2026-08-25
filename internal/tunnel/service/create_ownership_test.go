package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type createOp struct {
	MockOperator
	createErr  error
	deleteCall string
	gotCfg     tunnel.Config
}

func (o *createOp) Create(_ context.Context, cfg tunnel.Config) error {
	o.gotCfg = cfg
	return o.createErr
}

func (o *createOp) Delete(_ context.Context, stored *storage.AWGTunnel) error {
	o.deleteCall = stored.ID
	return nil
}

func kernelRecord() *storage.AWGTunnel {
	return &storage.AWGTunnel{
		ID: "awg10", Name: "t", Backend: "kernel",
		Interface: storage.AWGInterface{
			PrivateKey: "CA9lE1yLCcziI8Oq0dXDYr3QtdzFCEsKYw8sxAQ132o=",
			Address:    "10.8.0.2/32", MTU: 1280,
		},
		Peer: storage.AWGPeer{
			PublicKey:  "hOPHc7ZBk0dGrLLDFrCG7WHYzZ8SS5xBWMzOJ9CkNFo=",
			Endpoint:   "192.0.2.1:51820",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}
}

func serviceForCreate(t *testing.T, op *createOp) (*ServiceImpl, string, string) {
	t.Helper()
	dir := t.TempDir()
	confs := filepath.Join(dir, "confs")
	old := tunnel.ConfDir
	tunnel.ConfDir = confs
	t.Cleanup(func() { tunnel.ConfDir = old })

	tunnels := filepath.Join(dir, "tunnels")
	store := storage.NewAWGTunnelStoreWithLockDir(tunnels, filepath.Join(dir, "locks"))
	return &ServiceImpl{store: store, legacyOperator: op, state: NewMockStateManager()}, tunnels, confs
}

// Создание владеет всей последовательностью: ресурс в NDMS, запись, конфиг.
// Раньше запись и конфиг делал хендлер уже после возврата — и при их провале
// созданный ресурс оставался жить без записи, то есть никто уже не знал, что
// он наш, и никто его не убирал.
func TestCreateWritesRecordAndConf(t *testing.T) {
	op := &createOp{}
	s, tunnels, confs := serviceForCreate(t, op)

	if err := s.Create(context.Background(), kernelRecord()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tunnels, "awg10.json")); err != nil {
		t.Errorf("запись не сохранена: %v", err)
	}
	if _, err := os.Stat(filepath.Join(confs, "awg10.conf")); err != nil {
		t.Errorf("конфиг не записан: %v", err)
	}
}

// Провал сохранения обязан снести созданный ресурс: иначе он осиротеет.
func TestCreateRollsBackResourceWhenSaveFails(t *testing.T) {
	op := &createOp{}
	s, tunnels, _ := serviceForCreate(t, op)
	if err := os.MkdirAll(tunnels, 0o500); err != nil {
		t.Fatal(err)
	}

	err := s.Create(context.Background(), kernelRecord())
	if err == nil {
		t.Fatal("сохранение в каталог без прав обязано провалиться")
	}
	if op.deleteCall != "awg10" {
		t.Errorf("созданный ресурс обязан быть снесён, Delete позван для %q", op.deleteCall)
	}
}

// Провал записи конфига откатывает и запись, и ресурс — иначе останется
// туннель, для которого нечего применять.
func TestCreateRollsBackWhenConfFails(t *testing.T) {
	op := &createOp{}
	s, tunnels, confs := serviceForCreate(t, op)
	// Файл на месте каталога конфигов: MkdirAll обязан отказать.
	if err := os.MkdirAll(filepath.Dir(confs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(confs, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := s.Create(context.Background(), kernelRecord())
	if err == nil {
		t.Fatal("запись конфига обязана была провалиться")
	}
	if op.deleteCall != "awg10" {
		t.Errorf("ресурс обязан быть снесён, Delete позван для %q", op.deleteCall)
	}
	if _, statErr := os.Stat(filepath.Join(tunnels, "awg10.json")); !os.IsNotExist(statErr) {
		t.Errorf("запись обязана быть убрана, got %v", statErr)
	}
}

// Провал создания ресурса не должен ничего откатывать: откатывать нечего.
func TestCreateNoRollbackWhenResourceFails(t *testing.T) {
	op := &createOp{createErr: errors.New("NDMS отказал")}
	s, tunnels, _ := serviceForCreate(t, op)

	if err := s.Create(context.Background(), kernelRecord()); err == nil {
		t.Fatal("создание обязано было провалиться")
	}
	if op.deleteCall != "" {
		t.Errorf("сносить нечего, а Delete позван для %q", op.deleteCall)
	}
	if _, err := os.Stat(filepath.Join(tunnels, "awg10.json")); !os.IsNotExist(err) {
		t.Errorf("записи быть не должно: %v", err)
	}
}

func TestCreateRejectsDuplicate(t *testing.T) {
	op := &createOp{}
	s, _, _ := serviceForCreate(t, op)
	if err := s.store.Save(kernelRecord()); err != nil {
		t.Fatal(err)
	}

	err := s.Create(context.Background(), kernelRecord())
	if !errors.Is(err, tunnel.ErrAlreadyExists) {
		t.Errorf("ожидался ErrAlreadyExists, got %v", err)
	}
}

// Конфиг для оператора собирает сервис из записи каноническим StoredToConfig —
// хендлер клал туда четыре поля вручную, из-за чего ветка маршрута по
// умолчанию в операторе была недостижима.
func TestCreatePassesFullConfigFromRecord(t *testing.T) {
	op := &createOp{}
	s, _, _ := serviceForCreate(t, op)
	rec := kernelRecord()
	rec.DefaultRoute = true
	rec.Interface.Address = "10.8.0.2/24"

	if err := s.Create(context.Background(), rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !op.gotCfg.DefaultRoute {
		t.Error("маршрут по умолчанию из записи обязан доехать до оператора")
	}
	if op.gotCfg.Address != "10.8.0.2" || op.gotCfg.AddressPrefix != 24 {
		t.Errorf("адрес разобран неверно: %q/%d", op.gotCfg.Address, op.gotCfg.AddressPrefix)
	}
	if op.gotCfg.ID != "awg10" || op.gotCfg.Name != "t" {
		t.Errorf("идентичность потеряна: %q %q", op.gotCfg.ID, op.gotCfg.Name)
	}
}
