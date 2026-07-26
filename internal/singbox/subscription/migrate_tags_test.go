package subscription

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// Issue #614/#625. Расширение ключа меняет теги ровно у тех групп, где раньше
// разные серверы схлопывались в один. Исключения пользователя хранятся тегами,
// поэтому без переноса они молча перестали бы совпадать, и исключённые серверы
// вернулись бы в строй.

// wsFeed: два эндпоинта, различающиеся только транспортом, плюс независимый
// третий сервер (нужен, чтобы не срабатывал guard «исключены все»).
func wsFeed() (a, b, other vlink.ParsedOutbound) {
	return mkParsedWS("1.2.3.4", 443, "cdn.example.com", "/path1", "host1"),
		mkParsedWS("1.2.3.4", 443, "cdn.example.com", "/path2", "host2"),
		mkParsed("other.example.com", 443, "vless")
}

// narrowInfo — метаданные того единственного члена, который существовал до
// правки: узкий тег и общие для группы протокол/сервер/порт/SNI.
func narrowInfo(p vlink.ParsedOutbound) MemberInfo {
	mi := toMemberInfo(stableTagFromKey("sub", identityKey(p)), p)
	return mi
}

func TestRemapStaleTags_ExpandsCollapsedGroup(t *testing.T) {
	a, b, other := wsFeed()
	diff := ApplyDiff("sub", nil, []vlink.ParsedOutbound{a, b, other})
	old := narrowInfo(a)

	got, active, changed := remapStaleTags([]string{old.Tag}, []MemberInfo{old}, diff, "")
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if active != "" {
		t.Errorf("active = %q, want empty (was not set)", active)
	}
	for _, tag := range got {
		if tag == old.Tag {
			t.Errorf("stale tag %q survived the remap: %v", old.Tag, got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want both endpoints of the collapsed group", got)
	}
}

// Сервер ушёл от провайдера — тег протух не из-за схемы ключей. Трогать его
// нельзя: когда сервер вернётся, он обязан остаться исключённым.
func TestRemapStaleTags_KeepsTagWithoutMatchInFeed(t *testing.T) {
	_, _, other := wsFeed()
	diff := ApplyDiff("sub", nil, []vlink.ParsedOutbound{other})
	gone := MemberInfo{Tag: "sub-x-deadbeef", Protocol: "vless", Server: "gone.example.com", Port: 443}

	got, _, changed := remapStaleTags([]string{gone.Tag}, []MemberInfo{gone}, diff, "")
	if changed {
		t.Error("changed = true, want false")
	}
	if len(got) != 1 || got[0] != gone.Tag {
		t.Fatalf("got %v, want the tag preserved", got)
	}
}

func TestRemapStaleTags_Idempotent(t *testing.T) {
	a, b, other := wsFeed()
	diff := ApplyDiff("sub", nil, []vlink.ParsedOutbound{a, b, other})
	old := narrowInfo(a)

	first, _, _ := remapStaleTags([]string{old.Tag}, []MemberInfo{old}, diff, "")
	second, _, changed := remapStaleTags(first, []MemberInfo{old}, diff, "")
	if changed {
		t.Error("second pass reported a change, want no-op")
	}
	if len(second) != len(first) {
		t.Fatalf("second = %v, first = %v", second, first)
	}
}

// Протухший ActiveMember обязан переехать: BuildSelector подставляет
// memberTags[0] только когда default пустой, а принадлежность набору не
// проверяет — иначе в конфиг уедет ссылка на несуществующий outbound.
func TestRemapStaleTags_RemapsActiveMember(t *testing.T) {
	a, b, other := wsFeed()
	diff := ApplyDiff("sub", nil, []vlink.ParsedOutbound{a, b, other})
	old := narrowInfo(a)

	_, active, changed := remapStaleTags(nil, []MemberInfo{old}, diff, old.Tag)
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if active == old.Tag || active == "" {
		t.Fatalf("active = %q, want one of the new tags", active)
	}
	found := false
	for _, tg := range append(append([]TaggedOutbound{}, diff.New...), diff.Existing...) {
		if tg.Tag == active {
			found = true
		}
	}
	if !found {
		t.Errorf("active %q is not a current member tag", active)
	}
}

// Расширение «один тег → N» способно исключить вообще всех. Ручной путь такое
// запрещает (ErrAllMembersExcluded), значит refresh не имеет права создавать
// это состояние сам.
func TestRemapStaleTags_GuardAgainstExcludingEveryone(t *testing.T) {
	a, b, _ := wsFeed()
	diff := ApplyDiff("sub", nil, []vlink.ParsedOutbound{a, b})
	old := narrowInfo(a)

	got, _, changed := remapStaleTags([]string{old.Tag}, []MemberInfo{old}, diff, "")
	if changed {
		t.Error("changed = true, want the expansion skipped")
	}
	if len(got) != 1 || got[0] != old.Tag {
		t.Fatalf("got %v, want the original tag untouched", got)
	}
}
