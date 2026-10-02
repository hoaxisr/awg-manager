package query

import (
	"context"
	"testing"
)

// Ответ списка несёт start — seq ДО запроса — и no — номер своего полёта:
// хук, пришедший, пока список в полёте, start не сдвигает.
// Мутация: start снимать после fetchListMap → start == seq после хука, красный.
func TestRefreshList_AnswerCarriesStartAndNo(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	bg := &blockingGetter{Getter: fg, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	bg.gate = make(chan struct{})
	type res struct {
		ans *listAnswer
		err error
	}
	got := make(chan res, 1)
	go func() {
		ans, err := s.refreshList(context.Background(), nil)
		got <- res{ans, err}
	}()
	bg.waitBlocked(t)
	s.mu.RLock()
	before := s.seq
	s.mu.RUnlock()
	s.OnDestroyed("Wireguard0") // метка существования: seq++ во время полёта
	close(bg.gate)
	r := <-got
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.mu.RLock()
	after, flights := s.seq, s.flights
	s.mu.RUnlock()
	if after == before {
		t.Fatal("хук не сдвинул seq: тест холостой")
	}
	if r.ans.start != before {
		t.Errorf("start = %d, want %d (seq до запроса, хук после — %d)", r.ans.start, before, after)
	}
	if r.ans.no != flights {
		t.Errorf("no = %d, want %d (s.flights)", r.ans.no, flights)
	}

	bg.gate = nil
	second, err := s.refreshList(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.no != r.ans.no+1 {
		t.Errorf("второй список: no = %d, want %d", second.no, r.ans.no+1)
	}
}
