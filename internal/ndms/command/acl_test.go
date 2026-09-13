package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// aclBodyPoster отдаёт заранее заданные тела ответов по очереди (пустая
// очередь → `{}`), записывая parse-строки — ACL-примитивы читают вложенный
// status[], который обычный fakePoster не моделирует.
type aclBodyPoster struct {
	parses []string
	bodies []json.RawMessage
}

func (p *aclBodyPoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	if m, ok := payload.(map[string]any); ok {
		if s, ok := m["parse"].(string); ok {
			p.parses = append(p.parses, s)
		}
	}
	if len(p.bodies) > 0 {
		b := p.bodies[0]
		p.bodies = p.bodies[1:]
		return b, nil
	}
	return json.RawMessage(`{}`), nil
}

func nestedACLError(msg string) json.RawMessage {
	return json.RawMessage(`[{"parse":{"prompt":"(config)","status":[{"status":"error","ident":"Network::Acl","message":"` + msg + `"}]}}]`)
}

func newACLTestCommands(bodies ...json.RawMessage) (*InterfaceCommands, *aclBodyPoster) {
	return newACLTestCommandsRC(nil, bodies...)
}

// newACLTestCommandsRC — то же с заданным running-config: снятие permit-all
// читает список, чтобы отличить «в нём только наша строка» от «там правила
// пользователя» (F314). rcLines == nil — блока нет, то есть чистая ветка.
func newACLTestCommandsRC(rcLines []string, bodies ...json.RawMessage) (*InterfaceCommands, *aclBodyPoster) {
	poster := &aclBodyPoster{bodies: bodies}
	// Настоящий SaveCoordinator (Request не nil-safe — nil-wiring должен
	// падать громко); часовой debounce — save в тестах не летит.
	sc := NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil)
	fg := query.NewFakeGetter()
	body, _ := json.Marshal(map[string]any{"message": rcLines})
	fg.SetJSON("/show/running-config", string(body))
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewInterfaceCommands(poster, sc, q, nil), poster
}

func TestACLPrimitives_ParseForms(t *testing.T) {
	cmds, poster := newACLTestCommands()
	ctx := context.Background()
	if err := cmds.ACLPermitIP(ctx, "AWGM_X", "10.0.0.0", "255.255.255.0", "10.0.1.0", "255.255.255.0"); err != nil {
		t.Fatalf("ACLPermitIP: %v", err)
	}
	if err := cmds.ACLBind(ctx, "Wireguard1", "AWGM_X"); err != nil {
		t.Fatalf("ACLBind: %v", err)
	}
	if err := cmds.ACLAutoDelete(ctx, "AWGM_X"); err != nil {
		t.Fatalf("ACLAutoDelete: %v", err)
	}
	if err := cmds.ACLUnbind(ctx, "Wireguard1", "AWGM_X"); err != nil {
		t.Fatalf("ACLUnbind: %v", err)
	}
	if err := cmds.ACLRemove(ctx, "AWGM_X"); err != nil {
		t.Fatalf("ACLRemove: %v", err)
	}
	want := []string{
		"access-list AWGM_X permit ip 10.0.0.0 255.255.255.0 10.0.1.0 255.255.255.0",
		"interface Wireguard1 ip access-group AWGM_X in",
		"access-list AWGM_X auto-delete",
		"no interface Wireguard1 ip access-group AWGM_X in",
		"no access-list AWGM_X",
	}
	if len(poster.parses) != len(want) {
		t.Fatalf("parses: want %d, got %d: %v", len(want), len(poster.parses), poster.parses)
	}
	for i, w := range want {
		if poster.parses[i] != w {
			t.Errorf("parse[%d]: got %q, want %q", i, poster.parses[i], w)
		}
	}
}

// Вложенные status:"error" parse-ответов всплывают ошибкой (транспортный
// уровень их не видит — stand-verified формы 2026-07-16).
func TestACLPrimitives_NestedErrorSurfaces(t *testing.T) {
	cmds, _ := newACLTestCommands(nestedACLError("cannot enable auto-deletion for unreferenced lists."))
	err := cmds.ACLAutoDelete(context.Background(), "AWGM_X")
	if err == nil || !strings.Contains(err.Error(), "unreferenced") {
		t.Fatalf("nested NDMS error must surface, got %v", err)
	}
}

// SetPermitAllACL: последовательность permit→bind→auto-delete с конвенцией
// _WEBADMIN_; дубль permit (идемпотентный re-assert) толерируется.
func TestSetPermitAllACL_SequenceAndDuplicateTolerance(t *testing.T) {
	cmds, poster := newACLTestCommands(nestedACLError("a duplicate was found for the rule being set."))
	if err := cmds.SetPermitAllACL(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("SetPermitAllACL (duplicate permit): %v", err)
	}
	want := []string{
		"access-list _WEBADMIN_OpkgTun0 permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0",
		"interface OpkgTun0 ip access-group _WEBADMIN_OpkgTun0 in",
		"access-list _WEBADMIN_OpkgTun0 auto-delete",
	}
	if len(poster.parses) != len(want) {
		t.Fatalf("parses: want %d, got %d: %v", len(want), len(poster.parses), poster.parses)
	}
	for i, w := range want {
		if poster.parses[i] != w {
			t.Errorf("parse[%d]: got %q, want %q", i, poster.parses[i], w)
		}
	}
}

// SetPermitAllACLv6/RemovePermitAllACLv6: у NDMS под IPv6 ОТДЕЛЬНОЕ пространство
// списков — `ipv6 access-list` + `ipv6 access-group`, имя то же (форма снята с
// живого роутера 2026-08-11). Порядок и толерантность к дублю — как у v4.
func TestSetPermitAllACLv6_SequenceAndDuplicateTolerance(t *testing.T) {
	cmds, poster := newACLTestCommands(nestedACLError("a duplicate was found for the rule being set."))
	if err := cmds.SetPermitAllACLv6(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("SetPermitAllACLv6 (duplicate permit): %v", err)
	}
	want := []string{
		"ipv6 access-list _WEBADMIN_OpkgTun0 permit ipv6 ::/0 ::/0",
		"interface OpkgTun0 ipv6 access-group _WEBADMIN_OpkgTun0 in",
		"ipv6 access-list _WEBADMIN_OpkgTun0 auto-delete",
	}
	if len(poster.parses) != len(want) {
		t.Fatalf("parses: want %d, got %d: %v", len(want), len(poster.parses), poster.parses)
	}
	for i, w := range want {
		if poster.parses[i] != w {
			t.Errorf("parse[%d]: got %q, want %q", i, poster.parses[i], w)
		}
	}
}

// Снятие v6-пары: unbind + удаление списка. Обе команды идут ВСЕГДА — снятие
// best-effort, и провал unbind не должен оставлять список висеть.
func TestRemovePermitAllACLv6_UnbindsAndRemoves(t *testing.T) {
	cmds, poster := newACLTestCommands(nil)
	if err := cmds.RemovePermitAllACLv6(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("RemovePermitAllACLv6: %v", err)
	}
	want := []string{
		"no interface OpkgTun0 ipv6 access-group _WEBADMIN_OpkgTun0 in",
		"no ipv6 access-list _WEBADMIN_OpkgTun0",
	}
	if len(poster.parses) != len(want) {
		t.Fatalf("parses: want %d, got %d: %v", len(want), len(poster.parses), poster.parses)
	}
	for i, w := range want {
		if poster.parses[i] != w {
			t.Errorf("parse[%d]: got %q, want %q", i, poster.parses[i], w)
		}
	}
}

// НЕ-duplicate провал permit — жёсткая ошибка SetPermitAllACL: guard
// толерирует только дубль, реальная ошибка не должна проглатываться (ревью).
func TestSetPermitAllACL_RealPermitErrorFails(t *testing.T) {
	cmds, poster := newACLTestCommands(nestedACLError("argument parse error."))
	err := cmds.SetPermitAllACL(context.Background(), "OpkgTun0")
	if err == nil || !strings.Contains(err.Error(), "argument parse error") {
		t.Fatalf("real permit error must surface, got %v", err)
	}
	if len(poster.parses) != 1 {
		t.Errorf("must stop after failed permit (no bind/auto-delete), got %v", poster.parses)
	}
}

// Смешанный ответ (реальная ошибка + слово «duplicate» в другом смысле) не
// должен классифицироваться как безвредный дубль.
func TestIsACLDuplicate_ExactPhraseOnly(t *testing.T) {
	cmds, _ := newACLTestCommands(nestedACLError("duplicate interface index; argument parse error."))
	if err := cmds.SetPermitAllACL(context.Background(), "OpkgTun0"); err == nil {
		t.Fatal("error mentioning 'duplicate' without the NDMS duplicate-rule phrase must surface")
	}
}

func TestRemovePermitAllACL_Sequence(t *testing.T) {
	cmds, poster := newACLTestCommands()
	if err := cmds.RemovePermitAllACL(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("RemovePermitAllACL: %v", err)
	}
	want := []string{
		"no interface OpkgTun0 ip access-group _WEBADMIN_OpkgTun0 in",
		"no access-list _WEBADMIN_OpkgTun0",
	}
	if len(poster.parses) != len(want) {
		t.Fatalf("parses: want %d, got %d: %v", len(want), len(poster.parses), poster.parses)
	}
	for i, w := range want {
		if poster.parses[i] != w {
			t.Errorf("parse[%d]: got %q, want %q", i, poster.parses[i], w)
		}
	}
}

// «Привязки нет» (argument parse error — стенд 2026-09-05) — не отказ: обе
// команды уходят, RemovePermitAllACL возвращает nil. Раньше это был отказ,
// и wdtt лечил его перепроверкой, а managed падал бы на 5.00 (#828).
func TestRemovePermitAllACL_ToleratesNotBound(t *testing.T) {
	// aclBodyPoster отдаёт очередь тел; nestedACLError — готовый строитель
	// status:"error" (acl_test.go:34-36; ident ndmsStatusErrors не смотрит).
	cmds, poster := newACLTestCommands(
		nestedACLError("argument parse error."),
		nestedACLError("argument parse error."),
	)
	if err := cmds.RemovePermitAllACL(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("«нет привязки» обязано прощаться: %v", err)
	}
	if len(poster.parses) != 2 {
		t.Fatalf("обе команды обязаны уйти: %v", poster.parses)
	}
}

// Любая другая status-ошибка всплывает как раньше.
func TestRemovePermitAllACL_OtherErrorSurfaces(t *testing.T) {
	cmds, _ := newACLTestCommands(nestedACLError("access list is in use"))
	err := cmds.RemovePermitAllACL(context.Background(), "OpkgTun0")
	if err == nil || !strings.Contains(err.Error(), "router reported error: access list is in use") {
		t.Fatalf("err = %v", err)
	}
}

// Прошивки до 5.01 не знают v6-ACL: NDMS отвечает «no such command:
// access-list» / «no such command: access-group» (issue #828, лог репортёра с
// KeeneticOS 5.00.C.11.0-0). Разрешать там нечего — механизма нет, — поэтому
// отказ обязан быть безобидным: иначе он валит всё включение fakeip/policy-tun.
func TestSetPermitAllACLv6_UnsupportedFirmwareTolerated(t *testing.T) {
	cmds, _ := newACLTestCommands(
		nestedACLError("no such command: access-list."),
		nestedACLError("no such command: access-group."),
		nestedACLError("no such command: access-list."),
	)
	if err := cmds.SetPermitAllACLv6(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("SetPermitAllACLv6 на прошивке без v6-ACL: %v", err)
	}
	cmds, _ = newACLTestCommands(
		nestedACLError("no such command: access-group."),
		nestedACLError("no such command: access-list."),
	)
	if err := cmds.RemovePermitAllACLv6(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("RemovePermitAllACLv6 на прошивке без v6-ACL: %v", err)
	}
}

// Толерантность узкая: настоящий отказ v6-ACL по-прежнему всплывает.
func TestSetPermitAllACLv6_RealErrorStillFails(t *testing.T) {
	cmds, _ := newACLTestCommands(nestedACLError("argument parse error."))
	if err := cmds.SetPermitAllACLv6(context.Background(), "OpkgTun0"); err == nil {
		t.Fatal("ожидался отказ на argument parse error, получен nil")
	}
}

// F314/#879: в списке `_WEBADMIN_<iface>` лежат ЕЩЁ И правила межсетевого
// экрана пользователя (веб-морда роутера пишет их туда же). Если кроме нашей
// строки там есть что-то ещё — снимается ровно наша строка, а список и его
// привязка остаются: `no access-list` унёс бы и чужие правила.
func TestRemovePermitAllACL_ForeignRulesPresent_RemovesOnlyOurRule(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rc     []string
		remove func(*InterfaceCommands) error
		want   string
	}{
		{
			name: "v4",
			rc: []string{"access-list _WEBADMIN_OpkgTun0",
				"    permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0",
				"    permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0",
				"    auto-delete", "!"},
			remove: func(c *InterfaceCommands) error {
				return c.RemovePermitAllACL(context.Background(), "OpkgTun0")
			},
			want: "no access-list _WEBADMIN_OpkgTun0 permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0",
		},
		{
			name: "v6",
			rc: []string{"ipv6 access-list _WEBADMIN_OpkgTun0",
				"    permit ipv6 2001:db8::/32 ::/0",
				"    permit ipv6 ::/0 ::/0", "!"},
			remove: func(c *InterfaceCommands) error {
				return c.RemovePermitAllACLv6(context.Background(), "OpkgTun0")
			},
			want: "no ipv6 access-list _WEBADMIN_OpkgTun0 permit ipv6 ::/0 ::/0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(tc.rc)
			if err := tc.remove(cmds); err != nil {
				t.Fatalf("снятие: %v", err)
			}
			if len(poster.parses) != 1 || poster.parses[0] != tc.want {
				t.Fatalf("parses = %v, ждали ровно [%q]", poster.parses, tc.want)
			}
		})
	}
}

// Только наша строка (плюс флаг auto-delete, правилом он не считается) —
// список наш целиком: прежняя пара unbind + `no access-list`. Пустой список
// NDMS сам не убирает (стенд 5.01), поэтому оставлять его нельзя.
func TestRemovePermitAllACL_OnlyOurRule_RemovesWholeList(t *testing.T) {
	cmds, poster := newACLTestCommandsRC([]string{"access-list _WEBADMIN_OpkgTun0",
		"    permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0", "    auto-delete", "!"})
	if err := cmds.RemovePermitAllACL(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("RemovePermitAllACL: %v", err)
	}
	want := []string{
		"no interface OpkgTun0 ip access-group _WEBADMIN_OpkgTun0 in",
		"no access-list _WEBADMIN_OpkgTun0",
	}
	if len(poster.parses) != 2 || poster.parses[0] != want[0] || poster.parses[1] != want[1] {
		t.Fatalf("parses = %v, want %v", poster.parses, want)
	}
}

// Гонка с чужой правкой: нашей строки в списке уже нет. `no rule found to
// delete.` (стенд 5.01) — цель достигнута, а не отказ.
func TestRemovePermitAllACL_RuleAlreadyGone_Tolerated(t *testing.T) {
	cmds, _ := newACLTestCommandsRC([]string{"access-list _WEBADMIN_OpkgTun0",
		"    permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0",
		"    permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0", "!"},
		nestedACLError("no rule found to delete."))
	if err := cmds.RemovePermitAllACL(context.Background(), "OpkgTun0"); err != nil {
		t.Fatalf("«правила уже нет» обязано прощаться: %v", err)
	}
}

// Running-config не прочитан — не снимаем НИЧЕГО: решить, чей это список,
// нечем, а снести чужие правила необратимо.
func TestRemovePermitAllACL_RunningConfigUnreadable_TouchesNothing(t *testing.T) {
	cmds, poster := newACLTestCommandsRC(nil)
	fg := query.NewFakeGetter()
	fg.SetError("/show/running-config", errors.New("ndms boom"))
	cmds.queries = query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	err := cmds.RemovePermitAllACL(context.Background(), "OpkgTun0")
	if err == nil || !strings.Contains(err.Error(), "ndms boom") {
		t.Fatalf("err = %v, ждали отказ чтения running-config", err)
	}
	if len(poster.parses) != 0 {
		t.Fatalf("ни одной команды быть не должно, got %v", poster.parses)
	}
}
