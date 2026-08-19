package subscription

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

// Регрессия issue #745: тег члена подписки обязан пережить обновление, пока
// сервер остаётся тем же сервером. Ключ идентичности (diff.go) выводится из
// полей выдачи и уровнем зависит от состава набора, поэтому его хватает не
// всегда: панель ротирует reality short_id — теги едут; провайдер убрал один
// эндпоинт — уровень ключа перещёлкивается и теги едут у всей группы.
// Пользователь видит это как «сироты — не сироты»: сервер с теми же адресом,
// портом, SNI и именем объявлен сиротой и заведён заново.

const issue745UUID = "3a3b1c2e-9999-4321-aaaa-1234567890ab"

// issue745PanelBody лепит тело в духе типовой панели: один uuid на всю
// подписку, hosts×per записей, у каждой свой SNI и reality short_id. Общий
// uuid на хосте — обычное дело, именно он схлопывает записи в одну
// narrow-группу и включает расширенный ключ.
func issue745PanelBody(hosts, per int, sidFor func(h, i int) string) string {
	var b strings.Builder
	for h := 0; h < hosts; h++ {
		for i := 0; i < per; i++ {
			fmt.Fprintf(&b,
				"vless://%s@node%d.example.com:443?type=tcp&security=reality&pbk=aaaabbbbccccdddd&fp=chrome&sni=cdn%d-%d.example&sid=%s&flow=xtls-rprx-vision#Node%%20%d-%d\n",
				issue745UUID, h, h, i, sidFor(h, i), h, i)
		}
	}
	return b.String()
}

func issue745FixedSid(h, i int) string { return fmt.Sprintf("%02x%02x", h, i) }

// issue745Feed поднимает фид, тело которого берётся из bodyFn на каждый запрос.
func issue745Feed(t *testing.T, bodyFn func() string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(bodyFn()))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sortedTags(sub *Subscription) []string {
	out := append([]string(nil), sub.MemberTags...)
	sort.Strings(out)
	return out
}

func sameTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Неизменное тело — теги стабильны. Страховка от регрессии в самой схеме
// ключей: если сломается это, сломается всё остальное.
func TestIssue745_IdenticalFeedKeepsTags(t *testing.T) {
	svc, _ := newTestService(t)
	body := issue745PanelBody(5, 10, issue745FixedSid)
	url := issue745Feed(t, func() string { return body })
	sub, err := svc.Create(context.Background(), CreateInput{Label: "p", URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	want := sortedTags(sub)
	for n := 1; n <= 2; n++ {
		res, err := svc.Refresh(context.Background(), sub.ID)
		if err != nil {
			t.Fatal(err)
		}
		if res.Orphaned != 0 || res.Added != 0 {
			t.Errorf("обновление %d на неизменном теле: added=%d orphaned=%d, ожидались нули", n, res.Added, res.Orphaned)
		}
		after, _ := svc.Get(sub.ID)
		if got := sortedTags(after); !sameTags(got, want) {
			t.Errorf("обновление %d: теги поехали", n)
		}
	}
}

// Тот же набор в другом порядке — теги стабильны.
func TestIssue745_ReorderKeepsTags(t *testing.T) {
	svc, _ := newTestService(t)
	lines := strings.Split(strings.TrimSpace(issue745PanelBody(5, 10, issue745FixedSid)), "\n")
	rev := make([]string, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		rev = append(rev, lines[i])
	}
	var reordered bool
	url := issue745Feed(t, func() string {
		if reordered {
			return strings.Join(rev, "\n")
		}
		return strings.Join(lines, "\n")
	})
	sub, err := svc.Create(context.Background(), CreateInput{Label: "p", URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	want := sortedTags(sub)
	reordered = true
	res, err := svc.Refresh(context.Background(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Orphaned != 0 || res.Added != 0 {
		t.Errorf("перестановка выдачи: added=%d orphaned=%d, ожидались нули", res.Added, res.Orphaned)
	}
	after, _ := svc.Get(sub.ID)
	if got := sortedTags(after); !sameTags(got, want) {
		t.Errorf("перестановка выдачи: теги поехали")
	}
}

// Панель отдаёт новый reality short_id на каждой выдаче. Сервер тот же —
// адрес, порт, SNI, имя не менялись; sid маскировка, а не идентичность.
func TestIssue745_RotatedShortIDKeepsTags(t *testing.T) {
	svc, _ := newTestService(t)
	var gen int64
	url := issue745Feed(t, func() string {
		g := atomic.AddInt64(&gen, 1)
		return issue745PanelBody(5, 10, func(h, i int) string { return fmt.Sprintf("%02x%02x%02x", g, h, i) })
	})
	sub, err := svc.Create(context.Background(), CreateInput{Label: "p", URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	want := sortedTags(sub)
	for n := 1; n <= 2; n++ {
		res, err := svc.Refresh(context.Background(), sub.ID)
		if err != nil {
			t.Fatal(err)
		}
		if res.Orphaned != 0 || res.Added != 0 {
			t.Errorf("обновление %d с ротацией sid: added=%d orphaned=%d, ожидались нули", n, res.Added, res.Orphaned)
		}
		after, _ := svc.Get(sub.ID)
		if got := sortedTags(after); !sameTags(got, want) {
			t.Errorf("обновление %d с ротацией sid: теги поехали", n)
		}
	}
}

// Провайдер убрал ОДИН эндпоинт группы. Сиротой обязан стать только он:
// у соседей ни одно поле не изменилось, менять им теги не за что.
func TestIssue745_DroppedNeighborKeepsGroupTags(t *testing.T) {
	svc, _ := newTestService(t)
	mk := func(sni, path, name string) string {
		return fmt.Sprintf("vless://%s@node0.example.com:443?type=ws&path=%s&security=tls&sni=%s#%s",
			issue745UUID, path, sni, name)
	}
	n1 := mk("s1.example", "%2Fp1", "N1")
	n2 := mk("s1.example", "%2Fp2", "N2") // делит с N1 расширенный ключ, отличается транспортом
	n3 := mk("s2.example", "%2Fp1", "N3")
	n4 := mk("s3.example", "%2Fp1", "N4")
	var dropped bool
	url := issue745Feed(t, func() string {
		if dropped {
			return strings.Join([]string{n1, n3, n4}, "\n")
		}
		return strings.Join([]string{n1, n2, n3, n4}, "\n")
	})
	sub, err := svc.Create(context.Background(), CreateInput{Label: "p", URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{} // label → tag
	for _, m := range sub.Members {
		before[m.Label] = m.Tag
	}
	dropped = true
	res, err := svc.Refresh(context.Background(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 || res.Orphaned != 1 {
		t.Errorf("ушёл один эндпоинт: added=%d orphaned=%d, ожидалось added=0 orphaned=1", res.Added, res.Orphaned)
	}
	after, _ := svc.Get(sub.ID)
	for _, m := range after.Members {
		if before[m.Label] != m.Tag {
			t.Errorf("сосед %s сменил тег: было %s, стало %s", m.Label, before[m.Label], m.Tag)
		}
	}
	if len(after.OrphanTags) != 1 || after.OrphanTags[0] != before["N2"] {
		t.Errorf("сиротой объявлен не тот тег: %v, ожидался %s", after.OrphanTags, before["N2"])
	}
}

// Побочная находка: серверы, срезанные regex-фильтром, не попадают в
// MemberTags, поэтому каждое обновление объявляет их новыми — счётчик Added
// врёт, а в главный журнал на каждом плановом тике летит «+N new».
func TestIssue745_FilteredMembersAreNotAddedEveryRefresh(t *testing.T) {
	svc, _ := newTestService(t)
	body := issue745PanelBody(5, 10, issue745FixedSid)
	url := issue745Feed(t, func() string { return body })
	sub, err := svc.Create(context.Background(), CreateInput{Label: "p", URL: url, Enabled: true,
		FilterInclude: "Node [0-1]-"}) // оставляем 20 из 50
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.MemberTags) != 20 {
		t.Fatalf("фильтр должен оставить 20 членов, оставил %d", len(sub.MemberTags))
	}
	for n := 1; n <= 2; n++ {
		res, err := svc.Refresh(context.Background(), sub.ID)
		if err != nil {
			t.Fatal(err)
		}
		if res.Added != 0 {
			t.Errorf("обновление %d: added=%d — отфильтрованные серверы снова посчитаны новыми", n, res.Added)
		}
	}
}

// Два эндпоинта, различимые только полями, которых в MemberInfo нет (здесь —
// reality short_id): сопоставить их однозначно нечем. Такие теги переносить
// нельзя — иначе исключение или активный член молча переедут на чужой сервер.
func TestIssue745_AmbiguousMatchKeepsOrphans(t *testing.T) {
	svc, _ := newTestService(t)
	mk := func(sid, name string) string {
		return fmt.Sprintf("vless://%s@node0.example.com:443?type=tcp&security=reality&pbk=aaaabbbbccccdddd&fp=chrome&sni=s1.example&sid=%s#%s",
			issue745UUID, sid, name)
	}
	var rotated bool
	url := issue745Feed(t, func() string {
		if rotated {
			return strings.Join([]string{mk("cc01", "N1"), mk("cc02", "N2")}, "\n")
		}
		return strings.Join([]string{mk("aa01", "N1"), mk("aa02", "N2")}, "\n")
	})
	sub, err := svc.Create(context.Background(), CreateInput{Label: "p", URL: url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.MemberTags) != 2 {
		t.Fatalf("ожидалось 2 члена, получено %d", len(sub.MemberTags))
	}
	rotated = true
	res, err := svc.Refresh(context.Background(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Orphaned != 2 || res.Added != 2 {
		t.Errorf("неоднозначное сопоставление: added=%d orphaned=%d, ожидалось 2/2 (теги не переносим)", res.Added, res.Orphaned)
	}
}
