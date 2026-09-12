package main

import (
	"os"
	"strings"
	"testing"
)

// Проводку хуков однажды уже вырезали из setupOrchestrator заодно с соседним
// блоком: сборка и тесты этого не заметили, потому что пропущенный оператор
// молчит. wireHookNotifiers покрыт своим тестом, но он проверяет помощника, а
// не то, что его кто-то зовёт, — эта проверка закрывает именно вызов.
func TestSetupOrchestrator_WiresHookNotifiers(t *testing.T) {
	src, err := os.ReadFile("wiring_routing.go")
	if err != nil {
		t.Fatalf("чтение проводки: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (a *app) setupOrchestrator()")
	if start < 0 {
		t.Fatal("setupOrchestrator не найдена — проверку надо переписать под новое имя")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("не видно конца setupOrchestrator")
	}
	if !strings.Contains(body[start:start+end], "wireHookNotifiers(") {
		t.Fatal("setupOrchestrator не зовёт wireHookNotifiers: операторы останутся без источника ожидаемых хуков")
	}
}
