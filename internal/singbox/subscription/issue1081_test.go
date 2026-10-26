package subscription

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Регрессия issue #1081: сирота прошлого обновления обязана оставаться в
// учёте, пока её outbound лежит в слоте. Раньше OrphanTags перезаписывался
// сиротами ПОСЛЕДНЕГО обновления, старые поколения выпадали из учёта и
// копились в 40-subscriptions.json навсегда — DeleteOrphans их не видел.

// issue1081Gens поднимает фид, который на каждом обновлении отдаёт другое
// поколение серверов (провайдер ротирует хосты), и подписку на живом слоте.
func issue1081Gens(t *testing.T) (svc *Service, mut *fakeMutator, sub *Subscription, gen *string) {
	t.Helper()
	svc, mut = newTestService(t)
	mut.slot = map[string]bool{}
	g := "a"
	url := issue745Feed(t, func() string {
		var b strings.Builder
		for i := 0; i < 3; i++ {
			fmt.Fprintf(&b, "vless://%s@%s%d.example.com:443?type=tcp&security=tls&sni=%s%d.example#N%d\n", issue745UUID, g, i, g, i, i)
		}
		return b.String()
	})
	sub, err := svc.Create(context.Background(), CreateInput{Label: "p", URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return svc, mut, sub, &g
}

func issue1081Refresh(t *testing.T, svc *Service, id string) *Subscription {
	t.Helper()
	if _, err := svc.Refresh(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s, err := svc.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// slotTagsOf — outbound'ы подписки в слоте (без selector'а), отсортированные.
func slotTagsOf(mut *fakeMutator, sub *Subscription) []string {
	var out []string
	for t := range mut.slot {
		if strings.HasPrefix(t, sub.SelectorTag+"-") {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestIssue1081_OrphansOfAllGenerationsTracked(t *testing.T) {
	svc, mut, sub, gen := issue1081Gens(t)
	genA := sortedCopy(sub.MemberTags)
	*gen = "b"
	genB := sortedCopy(issue1081Refresh(t, svc, sub.ID).MemberTags)
	*gen = "c"
	after := issue1081Refresh(t, svc, sub.ID)

	want := sortedCopy(append(append([]string(nil), genA...), genB...))
	if got := sortedCopy(after.OrphanTags); !sameTags(got, want) {
		t.Fatalf("OrphanTags после A→B→C = %v, ожидались A+B %v", got, want)
	}

	if err := svc.DeleteOrphans(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	cleaned, _ := svc.Get(sub.ID)
	if got := slotTagsOf(mut, sub); !sameTags(got, sortedCopy(cleaned.MemberTags)) {
		t.Errorf("после DeleteOrphans в слоте %v, ожидались только члены %v", got, sortedCopy(cleaned.MemberTags))
	}
	if len(cleaned.OrphanTags) != 0 {
		t.Errorf("после DeleteOrphans OrphanTags = %v, ожидался пустой", cleaned.OrphanTags)
	}
}

// Вернувшийся в выдачу сервер снова член, а не сирота.
func TestIssue1081_ReturnedServerIsMemberNotOrphan(t *testing.T) {
	svc, _, sub, gen := issue1081Gens(t)
	genA := sortedCopy(sub.MemberTags)
	*gen = "b"
	genB := sortedCopy(issue1081Refresh(t, svc, sub.ID).MemberTags)
	*gen = "a"
	after := issue1081Refresh(t, svc, sub.ID)
	if got := sortedCopy(after.MemberTags); !sameTags(got, genA) {
		t.Errorf("члены = %v, ожидалось поколение A %v", got, genA)
	}
	if got := sortedCopy(after.OrphanTags); !sameTags(got, genB) {
		t.Errorf("сироты = %v, ожидалось поколение B %v", got, genB)
	}
}

// Удаление подписки снимает и outbound'ы её сирот (F726).
func TestIssue1081_DeleteRemovesOrphanOutbounds(t *testing.T) {
	svc, mut, sub, gen := issue1081Gens(t)
	*gen = "b"
	issue1081Refresh(t, svc, sub.ID)
	if err := svc.Delete(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if left := slotTagsOf(mut, sub); len(left) != 0 {
		t.Errorf("после удаления подписки в слоте остались её outbound'ы: %v", left)
	}
}

// Чужая подписка в том же слоте: её члены не сироты нашей и не снимаются.
func TestIssue1081_OtherSubscriptionUntouched(t *testing.T) {
	svc, mut, sub, gen := issue1081Gens(t)
	other, err := svc.Create(context.Background(), CreateInput{
		Label: "o", URL: issue745Feed(t, func() string { return issue745PanelBody(1, 2, issue745FixedSid) }), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	*gen = "b"
	issue1081Refresh(t, svc, sub.ID)
	if err := svc.DeleteOrphans(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if got := slotTagsOf(mut, other); !sameTags(got, sortedCopy(other.MemberTags)) {
		t.Errorf("члены чужой подписки в слоте = %v, ожидались %v", got, sortedCopy(other.MemberTags))
	}
}

// Мусор, выпавший из учёта до исправления (в слоте есть, в OrphanTags нет),
// DeleteOrphans снимает сразу, без промежуточного обновления.
func TestIssue1081_DeleteOrphansSeesUntrackedLeftovers(t *testing.T) {
	svc, mut, sub, _ := issue1081Gens(t)
	legacy := sub.SelectorTag + "-legacy00"
	mut.slot[legacy] = true
	if err := svc.DeleteOrphans(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if mut.slot[legacy] {
		t.Errorf("неучтённый остаток %s пережил DeleteOrphans", legacy)
	}
}
