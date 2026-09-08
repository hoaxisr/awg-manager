package installer

import "testing"

// RequiredTags — единственный источник тегов сборки для гейтов outbound-типов
// (naive, mieru, …); пустая константа молча отключила бы их все.
func TestRequiredTags_NotEmptyAndHasNaive(t *testing.T) {
	if len(RequiredTags) == 0 {
		t.Fatal("RequiredTags is empty — regen-embedded.sh must fill it")
	}
	for _, tag := range RequiredTags {
		if tag == "with_naive_outbound" {
			return
		}
	}
	t.Fatalf("RequiredTags %v lacks with_naive_outbound", RequiredTags)
}
