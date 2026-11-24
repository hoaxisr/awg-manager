package wdttdiag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

type fakeRecords struct {
	recs map[string]instancestore.Record
}

func (f fakeRecords) Get(key string) (instancestore.Record, bool) {
	r, ok := f.recs[key]
	return r, ok
}

type apiEnvelope struct {
	Success bool            `json:"success"`
	Error   bool            `json:"error"`
	Message string          `json:"message"`
	Code    string          `json:"code"`
	Data    json.RawMessage `json:"data"`
}

func decodeEnv(t *testing.T, rr *httptest.ResponseRecorder) apiEnvelope {
	t.Helper()
	var env apiEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal response %q: %v", rr.Body.String(), err)
	}
	return env
}

func clientRec(mode string) instancestore.Record {
	return instancestore.Record{
		ID:   "nl",
		Kind: instancestore.KindWdttClient,
		Name: "Нидерланды",
		WdttClient: &roles.WdttClientConfig{
			Mode:        mode,
			Listen:      "127.0.0.1:9001",
			Peer:        "203.0.113.10:56002",
			Password:    "client-secret-pw",
			DiagKey:     "stored-diag-key",
			VKHashes:    "vkhash1,vkhash2",
			Workers:     18,
			Obfs:        "srtp",
			Fingerprint: "chrome",
			DeviceID:    "dev-1",
			CaptchaMode: "rjs",
			VKAuthMode:  "vkcalls",
			NdmsIface:   "OpkgTun5",
			RawIface:    "opkgtun5",
		},
	}
}

func doPost(t *testing.T, h *Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/proxyrt/instances/"+key+"/diag", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.Serve(rr, req, key)
	return rr
}

func TestHandler_VerdictWithExitCode2Returns200AndSafeArgs(t *testing.T) {
	rec := clientRec("raw")
	var gotBinary string
	var gotArgs []string
	h := New(Deps{
		Records: fakeRecords{recs: map[string]instancestore.Record{rec.Key(): rec}},
		Binary: func(k instancestore.Kind) (string, bool) {
			if k != instancestore.KindWdttClient {
				return "", false
			}
			return "/opt/bin/wt-client", true
		},
		Runner: func(_ context.Context, binary string, args []string) (string, string, int, error) {
			gotBinary = binary
			gotArgs = append([]string(nil), args...)
			stdout := strings.Join([]string{
				`DIAG|{"ev":"start","run":1,"mode":"raw"}`,
				`DIAG|{"ev":"step","run":1,"step":"turn","title":"TURN VK","state":"ok","text":"udp:1.2.3.4:3478"}`,
				`DIAG|{"ev":"verdict","run":1,"code":"password_unknown","sev":"error","title":"Сервер не принял пароль","text":"Проверьте ссылку","actions":["Перевыпустите ссылку"],"facts":{"turn":"ok","detail":"pw=client-secret-pw dk=override-dk","attempts":2}}`,
				`DIAG|{"ev":"end","run":1}`,
			}, "\n")
			return stdout, "", 2, errors.New("exit status 2")
		},
	})

	rr := doPost(t, h, rec.Key(), `{"transport":"tcp","diagKey":"override-dk"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	env := decodeEnv(t, rr)
	var res Result
	if err := json.Unmarshal(env.Data, &res); err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 2 || res.Mode != "raw" || res.Transport != "tcp" {
		t.Fatalf("unexpected result metadata: %+v", res)
	}
	if res.Verdict == nil || res.Verdict.Sev != "error" || res.Verdict.Code != "password_unknown" {
		t.Fatalf("verdict = %+v", res.Verdict)
	}
	detailFact, _ := res.Verdict.Facts["detail"].(string)
	if strings.Contains(detailFact, "client-secret-pw") || strings.Contains(detailFact, "override-dk") || !strings.Contains(detailFact, "[REDACTED]") {
		t.Fatalf("verdict.Facts string was not redacted: %q", detailFact)
	}
	if gotBinary != "/opt/bin/wt-client" {
		t.Fatalf("binary = %q", gotBinary)
	}
	joined := strings.Join(gotArgs, " ")
	for _, banned := range []string{"--awgm-", "-listen", "-tun-name", "-diag-auto", "-diag-trigger", "-diag-ports"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("unexpected runtime flag %q in args: %v", banned, gotArgs)
		}
	}
	wantSubsets := [][]string{
		{"-diag"},
		{"-mode", "rawtun"},
		{"-peer", "203.0.113.10:56002"},
		{"-password", "client-secret-pw"},
		{"-vk", "vkhash1,vkhash2"},
		{"-diag-key", "override-dk"},
		{"-turn-transport", "tcp"},
		{"-vk-auth-mode", "vkcalls"},
	}
	for _, pair := range wantSubsets {
		found := false
		for i := 0; i+len(pair)-1 < len(gotArgs); i++ {
			if reflect.DeepEqual(gotArgs[i:i+len(pair)], pair) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing args %v in %v", pair, gotArgs)
		}
	}
}

func TestHandler_UsesStoredDiagKeyWhenOverrideEmpty(t *testing.T) {
	rec := clientRec("wg")
	var gotArgs []string
	h := New(Deps{
		Records: fakeRecords{recs: map[string]instancestore.Record{rec.Key(): rec}},
		Binary:  func(instancestore.Kind) (string, bool) { return "/opt/bin/wt-client", true },
		Runner: func(_ context.Context, _ string, args []string) (string, string, int, error) {
			gotArgs = args
			return `DIAG|{"ev":"verdict","run":1,"code":"path_ok","sev":"ok","title":"OK","text":"OK"}`, "", 0, nil
		},
	})
	rr := doPost(t, h, rec.Key(), `{}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	for i := 0; i+1 < len(gotArgs); i++ {
		if gotArgs[i] == "-mode" {
			t.Fatalf("wg mode must not pass -mode: %v", gotArgs)
		}
		if gotArgs[i] == "-diag-key" && gotArgs[i+1] == "stored-diag-key" {
			return
		}
	}
	t.Fatalf("expected stored -diag-key in %v", gotArgs)
}

func TestHandler_ValidationAndBinaryErrors(t *testing.T) {
	rec := clientRec("wg")
	incomplete := clientRec("wg")
	incomplete.ID = "incomplete"
	incomplete.WdttClient.VKHashes = ""
	srv := instancestore.Record{
		ID:         "srv",
		Kind:       instancestore.KindWdttServer,
		Name:       "Сервер",
		WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56002"},
	}

	recs := map[string]instancestore.Record{
		rec.Key():        rec,
		incomplete.Key(): incomplete,
		srv.Key():        srv,
	}

	t.Run("method not allowed", func(t *testing.T) {
		h := New(Deps{Records: fakeRecords{recs: recs}})
		req := httptest.NewRequest(http.MethodGet, "/api/proxyrt/instances/wdtt-client:nl/diag", nil)
		rr := httptest.NewRecorder()
		h.Serve(rr, req, rec.Key())
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d", rr.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		h := New(Deps{Records: fakeRecords{recs: recs}})
		rr := doPost(t, h, "wdtt-client:missing", ``)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d", rr.Code)
		}
	})

	t.Run("non client kind rejected", func(t *testing.T) {
		h := New(Deps{Records: fakeRecords{recs: recs}})
		rr := doPost(t, h, srv.Key(), ``)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rr.Code)
		}
	})

	t.Run("invalid transport", func(t *testing.T) {
		h := New(Deps{Records: fakeRecords{recs: recs}})
		if rr := doPost(t, h, rec.Key(), `{"transport":"quic"}`); rr.Code != http.StatusBadRequest {
			t.Fatalf("bad transport status=%d", rr.Code)
		}
	})

	t.Run("incomplete config", func(t *testing.T) {
		h := New(Deps{Records: fakeRecords{recs: recs}})
		rr := doPost(t, h, incomplete.Key(), ``)
		if rr.Code != http.StatusBadRequest || decodeEnv(t, rr).Code != "WDTT_DIAG_CONFIG_INCOMPLETE" {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("missing binary", func(t *testing.T) {
		h := New(Deps{
			Records: fakeRecords{recs: recs},
			Binary:  func(instancestore.Kind) (string, bool) { return "", false },
		})
		rr := doPost(t, h, rec.Key(), ``)
		if rr.Code != http.StatusConflict || decodeEnv(t, rr).Code != "WDTT_DIAG_NO_BINARY" {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("missing binary on disk for default runner", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "wt-client")
		h := New(Deps{
			Records: fakeRecords{recs: recs},
			Binary:  func(instancestore.Kind) (string, bool) { return missing, true },
		})
		rr := doPost(t, h, rec.Key(), ``)
		if rr.Code != http.StatusConflict || decodeEnv(t, rr).Code != "WDTT_DIAG_NO_BINARY" {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("sanity check failed")
		}
	})
}

func TestHandler_UnsupportedTimeoutBusyAndRedactedFailure(t *testing.T) {
	rec := clientRec("wg")
	recs := fakeRecords{recs: map[string]instancestore.Record{rec.Key(): rec}}
	bin := func(instancestore.Kind) (string, bool) { return "/opt/bin/wt-client", true }

	t.Run("unsupported old wt-client", func(t *testing.T) {
		h := New(Deps{
			Records: recs,
			Binary:  bin,
			Runner: func(context.Context, string, []string) (string, string, int, error) {
				return "", "flag provided but not defined: -diag\n", 2, errors.New("exit status 2")
			},
		})
		rr := doPost(t, h, rec.Key(), ``)
		if rr.Code != http.StatusConflict || decodeEnv(t, rr).Code != "WDTT_DIAG_UNSUPPORTED" {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("timeout returns 504", func(t *testing.T) {
		h := New(Deps{
			Records: recs,
			Binary:  bin,
			Timeout: 30 * time.Millisecond,
			Runner: func(ctx context.Context, _ string, _ []string) (string, string, int, error) {
				<-ctx.Done()
				return "", "", -1, ctx.Err()
			},
		})
		rr := doPost(t, h, rec.Key(), ``)
		if rr.Code != http.StatusGatewayTimeout || decodeEnv(t, rr).Code != "WDTT_DIAG_TIMEOUT" {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("request cancel stops run and frees slot", func(t *testing.T) {
		started := make(chan struct{}, 1)
		h := New(Deps{
			Records: recs,
			Binary:  bin,
			Timeout: 5 * time.Second,
			Runner: func(ctx context.Context, _ string, _ []string) (string, string, int, error) {
				started <- struct{}{}
				<-ctx.Done()
				return "", "", -1, ctx.Err()
			},
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-started
			cancel()
		}()
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		h.Serve(rr, req, rec.Key())
		if rr.Code != http.StatusRequestTimeout || decodeEnv(t, rr).Code != "WDTT_DIAG_CANCELED" {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if !h.acquire() {
			t.Fatal("слот не освобождён после отмены")
		}
	})

	t.Run("global single flight returns 409 busy even across distinct instances", func(t *testing.T) {
		secondRec := clientRec("wg")
		secondRec.ID = "de"
		multiRecs := fakeRecords{recs: map[string]instancestore.Record{
			rec.Key():       rec,
			secondRec.Key(): secondRec,
		}}

		started := make(chan struct{})
		var startedOnce sync.Once
		release := make(chan struct{})
		h := New(Deps{
			Records: multiRecs,
			Binary:  bin,
			Runner: func(context.Context, string, []string) (string, string, int, error) {
				startedOnce.Do(func() { close(started) })
				<-release
				return `DIAG|{"ev":"verdict","run":1,"code":"path_ok","sev":"ok","title":"OK","text":"OK"}`, "", 0, nil
			},
		})

		var wg sync.WaitGroup
		wg.Add(1)
		var firstCode int
		go func() {
			defer wg.Done()
			rr := doPost(t, h, rec.Key(), ``)
			firstCode = rr.Code
		}()
		<-started

		sameInst := doPost(t, h, rec.Key(), ``)
		if sameInst.Code != http.StatusConflict || decodeEnv(t, sameInst).Code != "WDTT_DIAG_BUSY" {
			t.Fatalf("same instance request status=%d body=%s", sameInst.Code, sameInst.Body.String())
		}
		otherInst := doPost(t, h, secondRec.Key(), ``)
		if otherInst.Code != http.StatusConflict || decodeEnv(t, otherInst).Code != "WDTT_DIAG_BUSY" {
			t.Fatalf("other instance request status=%d body=%s", otherInst.Code, otherInst.Body.String())
		}
		close(release)
		wg.Wait()
		if firstCode != http.StatusOK {
			t.Fatalf("first request status=%d", firstCode)
		}

		after := doPost(t, h, secondRec.Key(), ``)
		if after.Code != http.StatusOK {
			t.Fatalf("request after release status=%d body=%s", after.Code, after.Body.String())
		}
	})

	t.Run("failed without verdict redacts secrets", func(t *testing.T) {
		h := New(Deps{
			Records: recs,
			Binary:  bin,
			Runner: func(context.Context, string, []string) (string, string, int, error) {
				return "", "crash with password=client-secret-pw dk=stored-diag-key", 1, errors.New("exit 1")
			},
		})
		rr := doPost(t, h, rec.Key(), ``)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		env := decodeEnv(t, rr)
		if env.Code != "WDTT_DIAG_FAILED" {
			t.Fatalf("code=%q", env.Code)
		}
		if strings.Contains(env.Message, "client-secret-pw") || strings.Contains(env.Message, "stored-diag-key") {
			t.Fatalf("secret leaked in failure message: %q", env.Message)
		}
	})
}
