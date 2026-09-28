package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeMarker struct {
	markErr error
	marked  []string
}

func (f *fakeMarker) Mark(_ context.Context, n string) (string, error) {
	if f.markErr != nil {
		return "", f.markErr
	}
	f.marked = append(f.marked, n)
	return strings.ToLower(n), nil // как канонизация opkgtunN
}
func (f *fakeMarker) Unmark(context.Context, string) error { return nil }
func (f *fakeMarker) Candidates(context.Context) ([]ForeignIfaceCandidate, error) {
	return []ForeignIfaceCandidate{{Name: "csqtt0", Label: "csqtt0", Kind: "kernel"}}, nil
}

func foreignPost(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/interfaces/foreign/mark", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func TestForeignMark_RejectedIs400AndNoPublish(t *testing.T) {
	m := &fakeMarker{markErr: errors.Join(ErrForeignIfaceRejected, errors.New("ppp0 — интерфейс роутера"))}
	h := NewForeignIfaceHandler(m)
	published := false
	h.SetTunnelListPublisher(func(context.Context) { published = true })
	rr := foreignPost(h.Mark, `{"name":"ppp0"}`)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "FOREIGN_REJECTED") || published {
		t.Fatalf("code=%d body=%s published=%v", rr.Code, rr.Body.String(), published)
	}
}

func TestForeignMark_InternalErrorIs500(t *testing.T) {
	h := NewForeignIfaceHandler(&fakeMarker{markErr: errors.New("rci down")})
	if rr := foreignPost(h.Mark, `{"name":"csqtt0"}`); rr.Code != 500 {
		t.Fatalf("code = %d, ждали 500", rr.Code)
	}
}

func TestForeignMark_OKPublishes(t *testing.T) {
	m := &fakeMarker{}
	h := NewForeignIfaceHandler(m)
	published := false
	h.SetTunnelListPublisher(func(context.Context) { published = true })
	rr := foreignPost(h.Mark, `{"name":"OpkgTun7"}`)
	if rr.Code != 200 || !published || len(m.marked) != 1 {
		t.Fatalf("code=%d published=%v marked=%v", rr.Code, published, m.marked)
	}
	// R19: ответ несёт записанное имя — фронт выбирает его, а не набранное.
	var env struct {
		Data ForeignIfaceMarkResponse `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data != (ForeignIfaceMarkResponse{OK: true, Name: "opkgtun7"}) {
		t.Fatalf("data = %+v, body=%s", env.Data, rr.Body.String())
	}
}
