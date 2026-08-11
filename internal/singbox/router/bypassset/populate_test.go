package bypassset

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubGeo — таблица «тег → строки .dat»; отсутствующий тег отдаёт notFound.
type stubGeo struct {
	lines map[string][]string
	err   error
}

func (g stubGeo) GeoIPTagLines(tag string) ([]string, bool, error) {
	if g.err != nil {
		return nil, false, g.err
	}
	l, ok := g.lines[tag]
	if !ok {
		return nil, true, nil
	}
	return l, false, nil
}

// ipsetTrace — журнал вызовов стаба ipset: argv-строки и переданный stdin.
type ipsetTrace struct {
	t   *testing.T
	log string
}

// installIPSetStub кладёт стаб ipset, пишущий argv и stdin в файл, и делает
// его единственным кандидатом health-check'а (реальный ipset не запускается).
func installIPSetStub(t *testing.T) *ipsetTrace {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls.log")
	script := fmt.Sprintf(`#!/bin/sh
LOG=%q
echo "ARGV: $*" >> "$LOG"
case "$1" in
restore)
	while IFS= read -r line; do echo "STDIN: $line" >> "$LOG"; done
	;;
save)
	echo "create AWGM-BYPASS hash:net family inet maxelem 262144"
	echo "add AWGM-BYPASS 1.2.3.0/24"
	;;
list)
	if [ "$3" = "-t" ]; then
		echo "Name: AWGM-BYPASS"
		echo "Number of entries: 2"
	fi
	;;
esac
exit 0
`, log)
	bin := writeStubIPSet(t, script)
	withHealthTestEnv(t, []string{bin}, func(string) (bool, string) { return false, "" })
	return &ipsetTrace{t: t, log: log}
}

func (tr *ipsetTrace) lines(prefix string) []string {
	tr.t.Helper()
	raw, err := os.ReadFile(tr.log)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		tr.t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if s, ok := strings.CutPrefix(l, prefix); ok {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// argv — все вызовы ipset по порядку.
func (tr *ipsetTrace) argv() []string { return tr.lines("ARGV:") }

// stdin — всё, что ушло в `ipset restore` по порядку.
func (tr *ipsetTrace) stdin() []string { return tr.lines("STDIN:") }

// mutating — вызовы без идемпотентных create: последовательность, за которую
// отвечает сам Populate.
func (tr *ipsetTrace) mutating() []string {
	var out []string
	for _, a := range tr.argv() {
		if strings.HasPrefix(a, "create ") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func TestPopulate_FillsStagingAndSwaps(t *testing.T) {
	tr := installIPSetStub(t)
	geo := stubGeo{lines: map[string][]string{
		"ru": {"1.2.3.0/24", "2a00::/32", "5.5.5.5"},
	}}

	res, err := Populate(context.Background(), geo, PopulateInput{GeoIPTags: []string{"ru"}})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if len(res.MissingTags) != 0 {
		t.Errorf("MissingTags: want empty, got %v", res.MissingTags)
	}
	if res.EntryCount != 2 || !res.CountOK {
		t.Errorf("EntryCount/CountOK: want 2/true, got %d/%v", res.EntryCount, res.CountOK)
	}

	want := []string{
		"flush " + StagingSetName,
		"restore -exist",
		"swap " + SetName + " " + StagingSetName,
		"flush " + StagingSetName,
		"list " + SetName + " -t",
	}
	if got := tr.mutating(); !equalSeq(got, want) {
		t.Fatalf("call sequence:\nwant %v\ngot  %v", want, got)
	}
	// Оба набора создаются до того, как что-либо трогается.
	argv := tr.argv()
	if !strings.HasPrefix(argv[0], "create "+SetName+" ") || !strings.HasPrefix(argv[1], "create "+StagingSetName+" ") {
		t.Errorf("first two calls must create live then staging set, got %v", argv[:2])
	}

	wantStdin := []string{
		"add " + StagingSetName + " 1.2.3.0/24",
		"add " + StagingSetName + " 5.5.5.5/32",
	}
	if got := tr.stdin(); !equalSeq(got, wantStdin) {
		t.Errorf("restore input (IPv6 must be dropped):\nwant %v\ngot  %v", wantStdin, got)
	}
}

func TestPopulate_MissingTagCollected(t *testing.T) {
	tr := installIPSetStub(t)
	geo := stubGeo{lines: map[string][]string{"ru": {"1.2.3.0/24"}}}

	res, err := Populate(context.Background(), geo, PopulateInput{GeoIPTags: []string{"nosuch", "ru"}})
	if err != nil {
		t.Fatalf("missing tag must not fail Populate: %v", err)
	}
	if !equalSeq(res.MissingTags, []string{"nosuch"}) {
		t.Errorf("MissingTags: want [nosuch], got %v", res.MissingTags)
	}
	if !contains(tr.argv(), "swap "+SetName+" "+StagingSetName) {
		t.Error("swap must still happen for the tags that were found")
	}
	if got := tr.stdin(); !equalSeq(got, []string{"add " + StagingSetName + " 1.2.3.0/24"}) {
		t.Errorf("restore input: got %v", got)
	}
}

func TestPopulate_GeoErrorFails(t *testing.T) {
	tr := installIPSetStub(t)
	geo := stubGeo{err: errors.New("parse")}

	if _, err := Populate(context.Background(), geo, PopulateInput{GeoIPTags: []string{"ru"}}); err == nil {
		t.Fatal("want error from the geo source")
	}
	for _, a := range tr.argv() {
		if strings.HasPrefix(a, "swap ") {
			t.Fatalf("swap must not run after a geo error — live set must stay intact, calls: %v", tr.argv())
		}
	}
}

func TestPopulate_WritesSaveFile(t *testing.T) {
	tr := installIPSetStub(t)
	geo := stubGeo{lines: map[string][]string{"ru": {"1.2.3.0/24"}}}
	path := filepath.Join(t.TempDir(), "nested", "bypass.ipset")

	if _, err := Populate(context.Background(), geo, PopulateInput{GeoIPTags: []string{"ru"}, SavePath: path}); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("save file: %v", err)
	}
	if !strings.Contains(string(data), "add "+SetName+" 1.2.3.0/24") {
		t.Errorf("save file must hold the ipset dump, got %q", data)
	}
	// save идёт только после успешного swap.
	argv := tr.argv()
	swapAt, saveAt := indexPrefix(argv, "swap "), indexPrefix(argv, "save ")
	if swapAt < 0 || saveAt < 0 || saveAt < swapAt {
		t.Errorf("save must follow swap, calls: %v", argv)
	}
}

func TestPopulate_NoSavePathSkipsSave(t *testing.T) {
	tr := installIPSetStub(t)
	geo := stubGeo{lines: map[string][]string{"ru": {"1.2.3.0/24"}}}

	if _, err := Populate(context.Background(), geo, PopulateInput{GeoIPTags: []string{"ru"}}); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if indexPrefix(tr.argv(), "save ") >= 0 {
		t.Errorf("empty SavePath must not run `ipset save`, calls: %v", tr.argv())
	}
}

func equalSeq(a, b []string) bool {
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

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func indexPrefix(list []string, prefix string) int {
	for i, s := range list {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	return -1
}
