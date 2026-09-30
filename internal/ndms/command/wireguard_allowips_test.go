package command

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newWGCommandsWithResponse(body string) (*WireguardCommands, *fakePoster) {
	poster := &fakePoster{nextResp: json.RawMessage(body)}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	return NewWireguardCommands(poster, sc, q), poster
}

func TestAddPeerAllowIP_Payload(t *testing.T) {
	c, poster := newWGCommandsWithResponse(`{}`)
	if err := c.AddPeerAllowIP(context.Background(), confirmed(t, "Wireguard9"), "K=", "192.168.77.0", "255.255.255.0"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(poster.Payloads()[0])
	want := `{"interface":{"Wireguard9":{"wireguard":{"peer":[{"allow-ips":[{"address":"192.168.77.0","mask":"255.255.255.0"}],"key":"K="}]}}}}`
	if string(b) != want {
		t.Fatalf("payload:\n got %s\nwant %s", b, want)
	}
}

// Добавление ту же фразу НЕ терпит: `no such net` на добавлении — настоящий отказ.
func TestAddPeerAllowIP_DoesNotTolerateNoSuchNet(t *testing.T) {
	c, _ := newWGCommandsWithResponse(`{"interface":{"Wireguard9":{"wireguard":{"peer":[{"status":[{"status":"error","message":"\"Wireguard9\": no such net in peer \"K=\"."}]}]}}}}`)
	if err := c.AddPeerAllowIP(context.Background(), confirmed(t, "Wireguard9"), "K=", "192.168.77.0", "255.255.255.0"); err == nil {
		t.Fatal("отказ добавления проглочен")
	}
}

// 11.A/11.8: снятие отсутствующего элемента — `no such net in peer`; для
// снятия и отката это цель, а не отказ. Прочие отказы всплывают.
func TestRemovePeerAllowIP_ToleratesNoSuchNet(t *testing.T) {
	c, poster := newWGCommandsWithResponse(`{"interface":{"Wireguard9":{"wireguard":{"peer":[{"status":[{"status":"error","message":"\"Wireguard9\": no such net in peer \"K=\"."}]}]}}}}`)
	if err := c.RemovePeerAllowIP(context.Background(), confirmed(t, "Wireguard9"), "K=", "192.168.77.0", "255.255.255.0"); err != nil {
		t.Fatalf("отсутствующая сеть обязана терпеться: %v", err)
	}
	b, _ := json.Marshal(poster.Payloads()[0])
	if !strings.Contains(string(b), `{"address":"192.168.77.0","mask":"255.255.255.0","no":true}`) {
		t.Fatalf("payload: %s", b)
	}
	c, _ = newWGCommandsWithResponse(`{"status":[{"status":"error","message":"argument parse error"}]}`)
	if err := c.RemovePeerAllowIP(context.Background(), confirmed(t, "Wireguard9"), "K=", "192.168.77.0", "255.255.255.0"); err == nil {
		t.Fatal("настоящий отказ проглочен")
	}
}

// noSuchNetOnRemovePoster отвечает `no such net in peer` на снятие allow-ips и
// успехом на всё прочее.
type noSuchNetOnRemovePoster struct{ fakePoster }

func (p *noSuchNetOnRemovePoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	_, _ = p.fakePoster.Post(ctx, payload)
	if b, _ := json.Marshal(payload); strings.Contains(string(b), `"no":true`) {
		return json.RawMessage(`{"interface":{"Wireguard9":{"wireguard":{"peer":[{"status":[{"status":"error","message":"\"Wireguard9\": no such net in peer \"K=\"."}]}]}}}}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// Смена адреса пира: старого /32 на роутере уже нет — смена обязана пройти и
// поставить новый.
func TestUpdatePeerAllowIPs_ToleratesMissingOldIP(t *testing.T) {
	poster := &noSuchNetOnRemovePoster{}
	sc := NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: query.NewFakeGetter(), Logger: query.NopLogger()})
	c := NewWireguardCommands(poster, sc, q)
	if err := c.UpdatePeerAllowIPs(context.Background(), confirmed(t, "Wireguard9"), "K=", "10.9.0.2", "10.9.0.9"); err != nil {
		t.Fatalf("отсутствующий старый /32 уронил смену: %v", err)
	}
	payloads := poster.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("вызовов %d, ждали 2", len(payloads))
	}
	b, _ := json.Marshal(payloads[1])
	if !strings.Contains(string(b), `{"address":"10.9.0.9","mask":"255.255.255.255"}`) {
		t.Fatalf("новый /32 не поставлен: %s", b)
	}
}
