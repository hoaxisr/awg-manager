package dnsroute

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func newBackendTestSvc(t *testing.T) *ServiceImpl {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q, c, _, _ := newTestNDMS()
	return &ServiceImpl{store: store, queries: q, commands: c, resolver: &noopResolver{}}
}

// Правила HydraRoute панель больше не ведёт: запись с этим бэкендом
// отвергается, а не становится NDMS-правилом.
func TestHydraRouteBackend_Rejected(t *testing.T) {
	ctx := context.Background()
	svc := newBackendTestSvc(t)
	created, err := svc.Create(ctx, DomainList{Name: "ndms", ManualDomains: []string{"a.com"}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := svc.List(ctx)
	hr := DomainList{Name: "x", Backend: "hydraroute", ManualDomains: []string{"b.com"}}
	upd := hr
	upd.ID = created.ID

	for name, call := range map[string]func() error{
		"Create": func() error { _, err := svc.Create(ctx, hr); return err },
		"Update": func() error { _, err := svc.Update(ctx, upd); return err },
		"UpdateWith": func() error {
			_, err := svc.UpdateWith(ctx, created.ID, func(DomainList) (DomainList, error) { return hr, nil })
			return err
		},
		// Пакет отвергается целиком: обычная строка перед HR не записана.
		"CreateBatch": func() error {
			_, err := svc.CreateBatch(ctx, []DomainList{{Name: "ok", ManualDomains: []string{"c.com"}}, hr})
			return err
		},
		// Проверка раньше пропуска пустых строк: у HR-строки имя могло отсутствовать.
		"CreateBatch без имени": func() error {
			_, err := svc.CreateBatch(ctx, []DomainList{{Backend: "hydraroute", ManualDomains: []string{"c.com"}}})
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrHydraRouteBackend) {
			t.Errorf("%s: err = %v, want ErrHydraRouteBackend", name, err)
		}
	}
	if after, _ := svc.List(ctx); !reflect.DeepEqual(after, before) {
		t.Fatalf("список изменился: %+v → %+v", before, after)
	}
}

// Id HR-группы из старой вкладки или у агента — «не найдено».
func TestHydraRouteBackend_StaleID(t *testing.T) {
	ctx := context.Background()
	svc := newBackendTestSvc(t)
	const id = "hr:1:nwg0:0"
	if _, err := svc.Get(ctx, id); err == nil {
		t.Error("Get: ожидалась ошибка")
	}
	if _, err := svc.Update(ctx, DomainList{ID: id, Name: "x"}); err == nil {
		t.Error("Update: ожидалась ошибка")
	}
	if err := svc.SetEnabled(ctx, id, false); err == nil {
		t.Error("SetEnabled: ожидалась ошибка")
	}
	if err := svc.Delete(ctx, id); err == nil {
		t.Error("Delete: ожидалась ошибка")
	}
	if n, err := svc.DeleteBatch(ctx, []string{id}); n != 0 || err != nil {
		t.Errorf("DeleteBatch = %d, %v", n, err)
	}
}
