package dnsroute

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// randSubnet — случайная сеть из узкого пространства, чтобы чаще
// попадались вложенность, совпадения и дубли; иногда — мусор.
func randSubnet(r *rand.Rand) string {
	switch r.Intn(10) {
	case 0:
		return "not-a-cidr"
	case 1, 2, 3:
		// IPv6, в том числе v4-mapped.
		if r.Intn(4) == 0 {
			return fmt.Sprintf("::ffff:10.%d.%d.0/%d", r.Intn(4), r.Intn(4), 96+r.Intn(33))
		}
		return fmt.Sprintf("2a00:%x:%x::/%d", r.Intn(4), r.Intn(4), r.Intn(65))
	default:
		return fmt.Sprintf("%d.%d.%d.%d/%d", 1+r.Intn(3), r.Intn(4), r.Intn(4), r.Intn(4)*64, r.Intn(33))
	}
}

func randSubnets(r *rand.Rand, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = randSubnet(r)
	}
	return out
}

// TestDedupSubnets_MatchesOracle — индексная dedupSubnets даёт ровно то же,
// что прежний перебор: kept, порядок, счётчики, Items (F699).
func TestDedupSubnets_MatchesOracle(t *testing.T) {
	r := rand.New(rand.NewSource(699))
	// Счётчики — страж от холостого генератора: каждый путь должен встретиться.
	var crossList, internal, exact, covered, holesMattered int
	for c := 0; c < 5000; c++ {
		var lists []DomainList
		for i := 0; i < r.Intn(4); i++ {
			l := DomainList{ID: fmt.Sprintf("list_%d", i+1), Name: fmt.Sprintf("L%d", i+1), Subnets: randSubnets(r, r.Intn(12))}
			if r.Intn(2) == 0 {
				l.ExcludeSubnets = randSubnets(r, 1+r.Intn(3))
			}
			lists = append(lists, l)
		}
		cur := "list_cur"
		if len(lists) > 0 && r.Intn(3) == 0 {
			cur = lists[r.Intn(len(lists))].ID // текущий список среди lists — исключается
		}
		input := randSubnets(r, r.Intn(20))
		if len(lists) > 0 && len(lists[0].Subnets) > 0 && r.Intn(2) == 0 {
			input = append(input, lists[0].Subnets[r.Intn(len(lists[0].Subnets))])
		}
		if len(input) > 0 && r.Intn(2) == 0 {
			input = append(input, input[r.Intn(len(input))]) // дубль внутри списка
		}

		wantKept, wantRep := dedupSubnetsOracle(input, cur, "Cur", lists)
		gotKept, gotRep := dedupSubnets(input, cur, "Cur", lists)
		if !reflect.DeepEqual(gotKept, wantKept) || !reflect.DeepEqual(gotRep, wantRep) {
			t.Fatalf("случай %d расходится\ninput=%q cur=%s lists=%+v\nwant %q %+v\ngot  %q %+v",
				c, input, cur, lists, wantKept, wantRep, gotKept, gotRep)
		}
		for _, it := range wantRep.Items {
			if it.ListID == cur {
				internal++
			} else {
				crossList++
			}
			if it.Reason == "exact" {
				exact++
			} else {
				covered++
			}
		}
		noHoles := make([]DomainList, len(lists))
		for i, l := range lists {
			l.ExcludeSubnets = nil
			noHoles[i] = l
		}
		if k, _ := dedupSubnetsOracle(input, cur, "Cur", noHoles); !reflect.DeepEqual(k, wantKept) {
			holesMattered++
		}
	}
	t.Logf("cross=%d internal=%d exact=%d covered=%d holes=%d", crossList, internal, exact, covered, holesMattered)
	for name, n := range map[string]int{"cross": crossList, "internal": internal, "exact": exact, "covered": covered, "holes": holesMattered} {
		if n < 50 {
			t.Errorf("путь %s встретился %d раз — генератор холостой", name, n)
		}
	}
}

// subnetsScale — n разных /24 и /32 вперемешку: худший случай перебора.
func subnetsScale(n int) []string {
	out := make([]string, 0, n)
	for i := 0; len(out) < n; i++ {
		out = append(out, fmt.Sprintf("%d.%d.%d.0/24", 1+i/65536, i/256%256, i%256))
	}
	return out
}

func TestDedupSubnets_Scale20k(t *testing.T) {
	input := subnetsScale(MaxSubnetsPerList)
	other := DomainList{ID: "list_1", Subnets: subnetsScale(MaxSubnetsPerList)[:MaxSubnetsPerList/2]}
	start := time.Now()
	kept, rep := dedupSubnets(input, "list_2", "cur", []DomainList{other})
	took := time.Since(start)
	if len(kept) != MaxSubnetsPerList/2 || rep.ExactDupes != MaxSubnetsPerList/2 {
		t.Fatalf("kept=%d exact=%d", len(kept), rep.ExactDupes)
	}
	// Перебор тратил здесь секунды; индекс — миллисекунды. Порог с запасом
	// на -race и медленный CI.
	if took > 3*time.Second {
		t.Fatalf("20k подсетей: %v", took)
	}
	t.Logf("20k подсетей: %v", took)
}

func BenchmarkDedupSubnets20k(b *testing.B) {
	input := subnetsScale(MaxSubnetsPerList)
	lists := []DomainList{{ID: "list_1", Subnets: strings.Fields(strings.Join(subnetsScale(1000), " "))}}
	for b.Loop() {
		dedupSubnets(input, "list_2", "cur", lists)
	}
}
