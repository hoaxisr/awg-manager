package aiassistant

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryStore_FactsCRUD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ai-memory.json")

	store, err := NewMemoryStore(path)
	if err != nil {
		t.Fatalf("NewMemoryStore error = %v", err)
	}

	// 1. Add fact
	fact1 := store.AddFact("hardware", "Роутер KN-1811, ретранслятор Hero 4G+ на 192.168.50.85", "user")
	if fact1.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// 2. Add second fact
	fact2 := store.AddFact("network", "Шлюз провайдера 10.179.216.126", "user")
	if len(store.ListFacts("")) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(store.ListFacts("")))
	}

	// 3. Filter by category
	hwFacts := store.ListFacts("hardware")
	if len(hwFacts) != 1 || hwFacts[0].ID != fact1.ID {
		t.Fatalf("expected 1 hardware fact, got %v", hwFacts)
	}

	// 4. Deduplication
	dup := store.AddFact("hardware", "Роутер KN-1811, ретранслятор Hero 4G+ на 192.168.50.85", "user")
	if dup.ID != fact1.ID {
		t.Fatalf("expected duplicate fact to retain original ID, got %s vs %s", dup.ID, fact1.ID)
	}
	if len(store.ListFacts("")) != 2 {
		t.Fatalf("expected still 2 facts after deduplication, got %d", len(store.ListFacts("")))
	}

	// 5. Delete fact
	if !store.RemoveFact(fact2.ID) {
		t.Fatal("expected RemoveFact to return true")
	}
	if len(store.ListFacts("")) != 1 {
		t.Fatalf("expected 1 fact after delete, got %d", len(store.ListFacts("")))
	}

	// 6. Persistence
	store2, err := NewMemoryStore(path)
	if err != nil {
		t.Fatalf("reload MemoryStore error = %v", err)
	}
	if len(store2.ListFacts("")) != 1 {
		t.Fatalf("expected 1 fact reloaded from disk, got %d", len(store2.ListFacts("")))
	}
}

func TestMemoryStore_Playbooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ai-memory.json")

	store, err := NewMemoryStore(path)
	if err != nil {
		t.Fatalf("NewMemoryStore error = %v", err)
	}

	pb := store.AddOrUpdatePlaybook(LearnedPlaybook{
		Category:    "cloud",
		Title:       "Сбой Keenetic Co-Agent ретранслятора",
		Trigger:     "mws_cloud_timeout_5683",
		Diagnosis:   "Co-Agent пробует CoAP STUN порт 5683 через VPN",
		Action:      "add_cloud_ip",
		LearnedFrom: "cloud_gemini",
	})
	if pb.SuccessCount != 1 {
		t.Fatalf("expected successCount=1, got %d", pb.SuccessCount)
	}

	// Repeat same trigger & action -> increases successCount
	pb2 := store.AddOrUpdatePlaybook(LearnedPlaybook{
		Trigger: "mws_cloud_timeout_5683",
		Action:  "add_cloud_ip",
	})
	if pb2.SuccessCount != 2 {
		t.Fatalf("expected successCount=2, got %d", pb2.SuccessCount)
	}

	// Find playbook
	found := store.FindPlaybook("mws_cloud_timeout_5683")
	if found == nil || found.Action != "add_cloud_ip" {
		t.Fatalf("expected playbook found, got %+v", found)
	}

	// Partial substring match
	foundSub := store.FindPlaybook("ошибка mws_cloud_timeout")
	if foundSub == nil {
		t.Fatal("expected substring match for playbook")
	}

	// RenderPromptContext
	promptCtx := store.RenderPromptContext()
	if !strings.Contains(promptCtx, "mws_cloud_timeout_5683") {
		t.Fatalf("expected prompt context to contain trigger, got:\n%s", promptCtx)
	}
}

func TestMemoryStore_Journal(t *testing.T) {
	store, _ := NewMemoryStore("")
	for i := 0; i < 110; i++ {
		store.AddJournalEntry(LearningJournalEntry{
			Trigger: "anomaly",
			Query:   "What happened?",
		})
	}
	// Max cap at 100
	list := store.ListJournal(0)
	if len(list) != 100 {
		t.Fatalf("expected journal capped at 100 entries, got %d", len(list))
	}
}
