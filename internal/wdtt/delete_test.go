package wdtt

import "testing"

// TestService_DeleteClient проверяет семантику удаления: id уходит из конфига,
// повторное удаление даёт «не найден». Регрессия к выносу Stop() из-под s.mu.
func TestService_DeleteClient(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")

	inst, err := s.CreateClient(CreateClientInput{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteClient(inst.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if findClientIndex(cfg.Clients, inst.ID) >= 0 {
		t.Fatalf("клиент %q остался в конфиге после удаления", inst.ID)
	}

	if err := s.DeleteClient(inst.ID); err == nil {
		t.Fatal("повторное удаление должно вернуть «не найден»")
	}
}
