package systemtunnel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// ASC по отсутствующему интерфейсу — отказ до чтений по имени и до команды
// (`interface X wireguard asc` создал бы X); список не прочитан — тоже (F546).
func TestSetASCParams_AbsentOrListError_NoCommand(t *testing.T) {
	for _, listErr := range []error{nil, errors.New("rci down")} {
		// Интерфейс в кэше, но снят в NDMS мимо нас (хук не доехал): без
		// подтверждения чтения по имени ушли бы по снимку кэша.
		f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard5", Type: "Wireguard"})
		q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger()})
		if _, _, ok, err := q.Interfaces.Confirm(context.Background(), "Wireguard5"); err != nil || !ok {
			t.Fatalf("прогрев кэша: ok=%v err=%v", ok, err)
		}
		f.Remove("Wireguard5")
		f.FailList(listErr)
		cmds := command.NewCommands(command.Deps{
			Poster:  f,
			Queries: q,
			Save:    command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, nil),
		})
		svc := New(q, cmds, markedServers{}, logging.NewScopedLogger(nil, "tunnels", "asc"))
		err := svc.SetASCParams(context.Background(), "Wireguard5", json.RawMessage(`{"jc":4}`))
		if err == nil {
			t.Fatalf("listErr=%v: ждали отказ", listErr)
		}
		if listErr != nil && !errors.Is(err, listErr) {
			t.Fatalf("err=%v, want %v", err, listErr)
		}
		// Без проверки !ok отказ всё равно был бы (кэш выселен свежим
		// списком), но невнятный; текст — для человека.
		if listErr == nil && !strings.Contains(err.Error(), "интерфейса Wireguard5 нет в NDMS") {
			t.Fatalf("err=%v, want «интерфейса Wireguard5 нет в NDMS»", err)
		}
		if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
			t.Fatalf("listErr=%v: posts=%v E=%d фантомов=%d", listErr, f.Posts, f.E, f.Phantoms)
		}
	}
}
