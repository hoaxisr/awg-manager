package command

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	if err := cmds.ACLBind(ctx, confirmed(t, "Wireguard1"), "AWGM_X"); err != nil {
		t.Fatalf("ACLBind: %v", err)
	}
	if err := cmds.ACLAutoDelete(ctx, "AWGM_X"); err != nil {
		t.Fatalf("ACLAutoDelete: %v", err)
	}
	if err := cmds.ACLUnbind(ctx, confirmed(t, "Wireguard1"), "AWGM_X"); err != nil {
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
// _WEBADMIN_. Running-config без правила, а permit встречает дубль — гонка
// чтение→POST (F607): дубль толерируется.
func TestSetPermitAllACL_SequenceAndDuplicateTolerance(t *testing.T) {
	cmds, poster := newACLTestCommands(nestedACLError("a duplicate was found for the rule being set."))
	if err := cmds.SetPermitAllACL(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
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
// живого роутера 2026-08-11). Порядок и толерантность к дублю (гонка
// чтение→POST на running-config без правила, F607) — как у v4.
func TestSetPermitAllACLv6_SequenceAndDuplicateTolerance(t *testing.T) {
	cmds, poster := newACLTestCommands(nestedACLError("a duplicate was found for the rule being set."))
	if err := cmds.SetPermitAllACLv6(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
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

// НЕ-duplicate провал permit — жёсткая ошибка SetPermitAllACL: guard
// толерирует только дубль, реальная ошибка не должна проглатываться (ревью).
func TestSetPermitAllACL_RealPermitErrorFails(t *testing.T) {
	cmds, poster := newACLTestCommands(nestedACLError("argument parse error."))
	err := cmds.SetPermitAllACL(context.Background(), confirmed(t, "OpkgTun0"))
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
	if err := cmds.SetPermitAllACL(context.Background(), confirmed(t, "OpkgTun0")); err == nil {
		t.Fatal("error mentioning 'duplicate' without the NDMS duplicate-rule phrase must surface")
	}
}

// Гонка: running-config показал привязку без auto-delete, а к моменту POST её
// уже снял кто-то другой — «привязки нет» (argument parse error — стенд
// 2026-09-05) не отказ: обе команды уходят, RemovePermitAllACL возвращает nil.
func TestRemovePermitAllACL_ToleratesNotBound(t *testing.T) {
	// aclBodyPoster отдаёт очередь тел; nestedACLError — готовый строитель
	// status:"error" (ident ndmsStatusErrors не смотрит).
	cmds, poster := newACLTestCommandsRC(rcBoundNoAutoDelete(aclV4),
		nestedACLError("argument parse error."),
		nestedACLError("argument parse error."),
	)
	if err := cmds.RemovePermitAllACL(context.Background(), confirmed(t, "OpkgTun15")); err != nil {
		t.Fatalf("«нет привязки» обязано прощаться: %v", err)
	}
	if len(poster.parses) != 2 {
		t.Fatalf("обе команды обязаны уйти: %v", poster.parses)
	}
}

// Любая другая status-ошибка unbind всплывает, а remove всё равно уходит:
// снятие best-effort, висящий список без привязки хуже лишнего POST.
// Мутация: return на ошибке unbind до remove → один POST, красный.
func TestRemovePermitAllACL_OtherErrorSurfaces(t *testing.T) {
	cmds, poster := newACLTestCommandsRC(rcBoundNoAutoDelete(aclV4), nestedACLError("access list is in use"))
	err := cmds.RemovePermitAllACL(context.Background(), confirmed(t, "OpkgTun15"))
	if err == nil || !strings.Contains(err.Error(), "router reported error: access list is in use") {
		t.Fatalf("err = %v", err)
	}
	if want := []string{aclV4.unbindCmd(), aclV4.removeCmd()}; !slices.Equal(poster.parses, want) {
		t.Fatalf("parses = %v, ждали %v", poster.parses, want)
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
	if err := cmds.SetPermitAllACLv6(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
		t.Fatalf("SetPermitAllACLv6 на прошивке без v6-ACL: %v", err)
	}
}

// Толерантность узкая: настоящий отказ v6-ACL по-прежнему всплывает.
func TestSetPermitAllACLv6_RealErrorStillFails(t *testing.T) {
	cmds, _ := newACLTestCommands(nestedACLError("argument parse error."))
	if err := cmds.SetPermitAllACLv6(context.Background(), confirmed(t, "OpkgTun0")); err == nil {
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
				return c.RemovePermitAllACL(context.Background(), confirmed(t, "OpkgTun0"))
			},
			want: "no access-list _WEBADMIN_OpkgTun0 permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0",
		},
		{
			name: "v6",
			rc: []string{"ipv6 access-list _WEBADMIN_OpkgTun0",
				"    permit ipv6 2001:db8::/32 ::/0",
				"    permit ipv6 ::/0 ::/0", "!"},
			remove: func(c *InterfaceCommands) error {
				return c.RemovePermitAllACLs(context.Background(), confirmed(t, "OpkgTun0"))
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

// Гонка с чужой правкой: нашей строки в списке уже нет. `no rule found to
// delete.` (стенд 5.01) — цель достигнута, а не отказ.
func TestRemovePermitAllACL_RuleAlreadyGone_Tolerated(t *testing.T) {
	cmds, _ := newACLTestCommandsRC([]string{"access-list _WEBADMIN_OpkgTun0",
		"    permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0",
		"    permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0", "!"},
		nestedACLError("no rule found to delete."))
	if err := cmds.RemovePermitAllACL(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
		t.Fatalf("«правила уже нет» обязано прощаться: %v", err)
	}
}

// M2: в списке только чужие правила, нашей строки в свежем дереве нет (снята
// в веб-морде, реап-ретрай после частичного снятия) — ни одного POST: `no
// rule found to delete.` — ошибка разбора в status[] (кандидат в E), и
// уходила бы каждым тиком ретрая.
// Мутация: снять гейт hasOurPermitRule в ветке чужих правил → `no … permit`
// уходит, красный.
func TestRemovePermitAllACL_ForeignOnly_OurAbsent_NoPosts(t *testing.T) {
	foreign := map[string]string{"v4": "permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0", "v6": "permit ipv6 2001:db8::/32 ::/0"}
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(append(rcIface(f, true), f.header, "    "+foreign[f.name], "    auto-delete", "!"))
			if err := f.remove(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("снятие: %v", err)
			}
			if len(poster.parses) != 0 {
				t.Fatalf("нашего правила в дереве нет — ни одного POST, got %v", poster.parses)
			}
		})
	}
}

// Running-config не прочитан — не снимаем НИЧЕГО: решить, чей это список,
// нечем, а снести чужие правила необратимо.
func TestRemovePermitAllACL_RunningConfigUnreadable_TouchesNothing(t *testing.T) {
	cmds, poster := newACLTestCommandsRC(nil)
	fg := query.NewFakeGetter()
	fg.SetError("/show/running-config", errors.New("ndms boom"))
	cmds.queries = query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	err := cmds.RemovePermitAllACL(context.Background(), confirmed(t, "OpkgTun0"))
	if err == nil || !strings.Contains(err.Error(), "ndms boom") {
		t.Fatalf("err = %v, ждали отказ чтения running-config", err)
	}
	if len(poster.parses) != 0 {
		t.Fatalf("ни одной команды быть не должно, got %v", poster.parses)
	}
}

// Точечное снятие одного permit ip: форма `no access-list <acl> permit ip …`
// (стенд 5.01, 12.09); правила уже нет — цель достигнута, прочие отказы всплывают.
func TestACLRemovePermitIP(t *testing.T) {
	cmds, poster := newACLTestCommands(json.RawMessage(`{}`), nestedACLError("no rule found to delete."), nestedACLError("argument parse error."))
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := cmds.ACLRemovePermitIP(ctx, "AWGM_X", "192.168.77.0", "255.255.255.0", "10.0.1.0", "255.255.255.0"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := cmds.ACLRemovePermitIP(ctx, "AWGM_X", "192.168.77.0", "255.255.255.0", "10.0.1.0", "255.255.255.0"); err == nil || !strings.Contains(err.Error(), "argument parse error") {
		t.Fatalf("прочий отказ обязан всплыть, got %v", err)
	}
	if want := "no access-list AWGM_X permit ip 192.168.77.0 255.255.255.0 10.0.1.0 255.255.255.0"; poster.parses[0] != want {
		t.Fatalf("parse = %q, want %q", poster.parses[0], want)
	}
}

// v4-снятие нашей строки «нет такой команды» не глотает: команды v4-ACL есть
// на всех прошивках, отказ — настоящая поломка (терпимость isACLUnsupported
// только у v6).
func TestRemovePermitAllACL_V4NoSuchCommandSurfaces(t *testing.T) {
	cmds, _ := newACLTestCommandsRC([]string{"access-list _WEBADMIN_OpkgTun0",
		"    permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0",
		"    permit ip 0.0.0.0 0.0.0.0 0.0.0.0 0.0.0.0", "!"},
		nestedACLError("no such command: access-list."))
	if err := cmds.RemovePermitAllACL(context.Background(), confirmed(t, "OpkgTun0")); err == nil || !strings.Contains(err.Error(), "no such command") {
		t.Fatalf("err = %v, ждали отказ", err)
	}
}

// F606 (П17): снятие permit-all решается по тройке (bound, listed,
// auto-delete) из одного свежего running-config. Формы блоков — дословно со
// стенда (Task 59, П5, 5.01.C.6): unbind без привязки даёт E `argument parse
// error`, remove по отсутствующему списку молчит, auto-delete снимает список
// вместе с последней привязкой.
type aclFamily struct {
	name, group, header, rule string
	remove                    func(*InterfaceCommands, query.Confirmed) error
}

var (
	aclV4 = aclFamily{"v4", "ip access-group", "access-list _WEBADMIN_OpkgTun15", permitAllRuleV4,
		func(c *InterfaceCommands, i query.Confirmed) error {
			return c.RemovePermitAllACL(context.Background(), i)
		}}
	aclV6 = aclFamily{"v6", "ipv6 access-group", "ipv6 access-list _WEBADMIN_OpkgTun15", permitAllRuleV6,
		func(c *InterfaceCommands, i query.Confirmed) error {
			// Публичного «только v6» нет; дерево читается так же, как в RemovePermitAllACLs.
			lines, err := c.freshACLTree(context.Background(), i.Name())
			if err != nil {
				return err
			}
			return c.removePermitAll(context.Background(), i.Name(), lines, permitV6)
		}}
)

func (f aclFamily) unbindCmd() string {
	return "no interface OpkgTun15 " + f.group + " _WEBADMIN_OpkgTun15 in"
}
func (f aclFamily) removeCmd() string { return "no " + f.header }

func rcIface(f aclFamily, bound bool) []string {
	out := []string{"interface OpkgTun15", "    security-level public"}
	if bound {
		out = append(out, "    "+f.group+" _WEBADMIN_OpkgTun15 in")
	}
	return append(out, "    down")
}

func rcList(f aclFamily, autoDelete bool) []string {
	out := []string{f.header, "    " + f.rule}
	if autoDelete {
		out = append(out, "    auto-delete")
	}
	return out
}

func rcBoundNoAutoDelete(f aclFamily) []string {
	return append(rcIface(f, true), rcList(f, false)...)
}

// Ни привязки, ни списка (policy-tun без v6; повторное выключение) — ни
// одного POST: unbind здесь — ровно E F606.
// Мутация: снять гейт bound у unbind → unbind уходит, красный.
func TestRemovePermitAllACL_NotBound_NotListed_NoPosts(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(rcIface(f, false))
			if err := f.remove(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("снятие: %v", err)
			}
			if len(poster.parses) != 0 {
				t.Fatalf("ни одного POST, got %v", poster.parses)
			}
		})
	}
}

// Привязка + auto-delete (наша штатная форма) — только unbind: список NDMS
// снимает сам (П5: блока после unbind нет).
// Мутация: слать remove и в этой ветке → лишний POST, красный.
func TestRemovePermitAllACL_Bound_AutoDelete_UnbindOnly(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(append(rcIface(f, true), rcList(f, true)...))
			if err := f.remove(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("снятие: %v", err)
			}
			if want := []string{f.unbindCmd()}; !slices.Equal(poster.parses, want) {
				t.Fatalf("parses = %v, ждали %v", poster.parses, want)
			}
		})
	}
}

// Привязка без auto-delete — unbind, затем remove: пустой список NDMS сам не
// убирает (стенд 5.01, 2026-09-12).
// Мутация: autoDelete всегда true → remove не уходит, красный.
func TestRemovePermitAllACL_Bound_NoAutoDelete_UnbindAndRemove(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(rcBoundNoAutoDelete(f))
			if err := f.remove(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("снятие: %v", err)
			}
			if want := []string{f.unbindCmd(), f.removeCmd()}; !slices.Equal(poster.parses, want) {
				t.Fatalf("parses = %v, ждали %v", poster.parses, want)
			}
		})
	}
}

// Список есть, привязки нет — только remove.
// Мутация: listed всегда false → ни одного POST, красный.
func TestRemovePermitAllACL_ListedNotBound_RemoveOnly(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(append(rcIface(f, false), rcList(f, true)...))
			if err := f.remove(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("снятие: %v", err)
			}
			if want := []string{f.removeCmd()}; !slices.Equal(poster.parses, want) {
				t.Fatalf("parses = %v, ждали %v", poster.parses, want)
			}
		})
	}
}

// Привязка другого семейства не считается: v6-снятие на дереве с одной
// v4-парой и v4-снятие на дереве с одной v6-парой — ни одного POST.
// Мутация: InterfaceAccessGroupsOf без семейства → unbind уходит, красный.
func TestRemovePermitAllACL_OtherFamilyIgnored(t *testing.T) {
	v4tree := append(rcIface(aclV4, true), rcList(aclV4, true)...)
	v6tree := append(rcIface(aclV6, true), rcList(aclV6, true)...)
	for _, tc := range []struct {
		f  aclFamily
		rc []string
	}{{aclV6, v4tree}, {aclV4, v6tree}} {
		t.Run(tc.f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(tc.rc)
			if err := tc.f.remove(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("снятие: %v", err)
			}
			if len(poster.parses) != 0 {
				t.Fatalf("ни одного POST, got %v", poster.parses)
			}
		})
	}
}

// Q6: дерево читается свежим перед записью, а не из кэша — привязку снимает
// каскад auto-delete и веб-морда, хук об этом не приходит. Кэш помнит
// привязку, роутер — уже нет: ни одного POST.
// Мутация: Fetch → Lines (кэш) → unbind по устаревшему дереву, красный.
func TestRemovePermitAllACL_ReadsFreshTree(t *testing.T) {
	cmds, poster := newACLTestCommandsRC(nil)
	fg := query.NewFakeGetter()
	setRC := func(lines []string) {
		body, _ := json.Marshal(map[string]any{"message": lines})
		fg.SetJSON("/show/running-config", string(body))
	}
	cmds.queries.RunningConfig = query.NewRunningConfigStore(fg, query.NopLogger())
	setRC(append(rcIface(aclV6, true), rcList(aclV6, true)...))
	if _, err := cmds.queries.RunningConfig.Lines(context.Background()); err != nil {
		t.Fatal(err)
	}
	setRC(rcIface(aclV6, false))
	if err := cmds.RemovePermitAllACLs(context.Background(), confirmed(t, "OpkgTun15")); err != nil {
		t.Fatalf("снятие: %v", err)
	}
	if len(poster.parses) != 0 {
		t.Fatalf("решение по устаревшему кэшу: %v", poster.parses)
	}
}

// Снятие обоих семейств — одно чтение running-config (≈89 тиков ndm) на оба;
// решение по нему верное для обоих (v4-POST'ы v6-блоков не меняют).
// Мутация: RemovePermitAllACLs читает дерево на каждое семейство → 2, красный.
func TestRemovePermitAllACLs_OneFetchBothFamilies(t *testing.T) {
	cmds, poster := newACLTestCommandsRC(nil)
	fg := query.NewFakeGetter()
	body, _ := json.Marshal(map[string]any{"message": append(append(rcIface(aclV4, true), "    ipv6 access-group _WEBADMIN_OpkgTun15 in"),
		append(rcList(aclV4, true), rcList(aclV6, true)...)...)})
	fg.SetJSON("/show/running-config", string(body))
	cmds.queries.RunningConfig = query.NewRunningConfigStore(fg, query.NopLogger())
	if err := cmds.RemovePermitAllACLs(context.Background(), confirmed(t, "OpkgTun15")); err != nil {
		t.Fatalf("снятие: %v", err)
	}
	if n := fg.Calls("/show/running-config"); n != 1 {
		t.Fatalf("чтений running-config = %d, ждали 1", n)
	}
	if want := []string{aclV4.unbindCmd(), aclV6.unbindCmd()}; !slices.Equal(poster.parses, want) {
		t.Fatalf("parses = %v, ждали %v", poster.parses, want)
	}
}

// Провал v4 не мешает снять v6; наружу — ошибка v4.
// Мутация: вернуть ошибку v4 до v6 → v6-unbind не уходит, красный.
func TestRemovePermitAllACLs_V4FailureStillRemovesV6(t *testing.T) {
	rc := append(append(rcIface(aclV4, true), "    ipv6 access-group _WEBADMIN_OpkgTun15 in"),
		append(rcList(aclV4, true), rcList(aclV6, true)...)...)
	cmds, poster := newACLTestCommandsRC(rc, nestedACLError("access list is in use"))
	err := cmds.RemovePermitAllACLs(context.Background(), confirmed(t, "OpkgTun15"))
	if err == nil || !strings.Contains(err.Error(), "access list is in use") {
		t.Fatalf("err = %v", err)
	}
	if want := []string{aclV4.unbindCmd(), aclV6.unbindCmd()}; !slices.Equal(poster.parses, want) {
		t.Fatalf("parses = %v, ждали %v", poster.parses, want)
	}
}

// F607 (П18, В2): permit уходит только при отсутствии нашего правила в свежем
// running-config. Идемпотентной формы permit у NDMS нет (стенд X2: повтор и
// повтор с лишними пробелами — E duplicate, JSON-формы — `no input`), а
// повтор bind и auto-delete идёт без E — их шлём как раньше.
func (f aclFamily) set(c *InterfaceCommands, i query.Confirmed) error {
	if f.name == "v6" {
		return c.SetPermitAllACLv6(context.Background(), i)
	}
	return c.SetPermitAllACL(context.Background(), i)
}

func (f aclFamily) permitCmd() string { return f.header + " " + f.rule }
func (f aclFamily) bindCmd() string {
	return "interface OpkgTun15 " + f.group + " _WEBADMIN_OpkgTun15 in"
}
func (f aclFamily) autoDeleteCmd() string { return f.header + " auto-delete" }

// rcStore подменяет running-config командного слоя своим FakeGetter'ом —
// чтобы считать чтения и менять дерево между ними.
func rcStore(cmds *InterfaceCommands, lines []string) *query.FakeGetter {
	fg := query.NewFakeGetter()
	setRCLines(fg, lines)
	cmds.queries.RunningConfig = query.NewRunningConfigStore(fg, query.NopLogger())
	return fg
}

func setRCLines(fg *query.FakeGetter, lines []string) {
	body, _ := json.Marshal(map[string]any{"message": lines})
	fg.SetJSON("/show/running-config", string(body))
}

// Правило стоит — permit не шлётся (это и есть 2 E на включение, X2); bind и
// auto-delete уходят: они идемпотентны без E и чинят снятую привязку.
// Чтение — ровно одно на вызов (≈89 тиков ndm).
// Мутация: убрать чтение (permit всегда) → три POST, красный.
func TestSetPermitAllACL_RulePresent_NoPermitPost(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(nil)
			fg := rcStore(cmds, append(rcIface(f, true), rcList(f, true)...))
			if err := f.set(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("постановка: %v", err)
			}
			if want := []string{f.bindCmd(), f.autoDeleteCmd()}; !slices.Equal(poster.parses, want) {
				t.Fatalf("parses = %v, ждали %v", poster.parses, want)
			}
			if n := fg.Calls("/show/running-config"); n != 1 {
				t.Fatalf("чтений running-config = %d, ждали 1", n)
			}
		})
	}
}

// Правила нет (список снят мимо нас, апгрейд, первое включение) — полная
// тройка permit → bind → auto-delete.
// Мутация: permit не шлётся никогда → два POST, красный.
func TestSetPermitAllACL_RuleAbsent_FullSequence(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(rcIface(f, false))
			if err := f.set(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("постановка: %v", err)
			}
			if want := []string{f.permitCmd(), f.bindCmd(), f.autoDeleteCmd()}; !slices.Equal(poster.parses, want) {
				t.Fatalf("parses = %v, ждали %v", poster.parses, want)
			}
		})
	}
}

// Running-config не прочитан — не шлём НИЧЕГО, ошибка наверх: one-shot ассерт
// флаг не взведёт и повторит следующим тиком (L4).
// Мутация: игнорировать ошибку чтения → permit ушёл, красный.
func TestSetPermitAllACL_RunningConfigUnreadable_NoPosts(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(nil)
			fg := rcStore(cmds, nil)
			fg.SetError("/show/running-config", errors.New("ndms boom"))
			err := f.set(cmds, confirmed(t, "OpkgTun15"))
			if err == nil || !strings.Contains(err.Error(), "ndms boom") {
				t.Fatalf("err = %v, ждали отказ чтения running-config", err)
			}
			if len(poster.parses) != 0 {
				t.Fatalf("ни одной команды быть не должно, got %v", poster.parses)
			}
		})
	}
}

// Решение по свежему дереву, не по кэшу: кэш помнит правило, а список уже
// снят мимо нас (веб-морда, каскад auto-delete) — permit обязан уйти, иначе
// интерфейс остаётся без разрешения.
// Мутация: Fetch → Lines (кэш) → permit не уходит, красный.
func TestSetPermitAllACL_ReadsFreshTree(t *testing.T) {
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(nil)
			fg := rcStore(cmds, append(rcIface(f, true), rcList(f, true)...))
			if _, err := cmds.queries.RunningConfig.Lines(context.Background()); err != nil {
				t.Fatal(err)
			}
			setRCLines(fg, rcIface(f, false))
			if err := f.set(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("постановка: %v", err)
			}
			if len(poster.parses) == 0 || poster.parses[0] != f.permitCmd() {
				t.Fatalf("решение по устаревшему кэшу: %v", poster.parses)
			}
		})
	}
}

// В списке только ЧУЖОЕ правило — наше не стоит: permit уходит (рядом с
// чужим), а когда наше встало — следующий вызов его не повторяет.
// Мутация: «правило стоит» = «блок есть» → permit не уходит, красный;
// предикат всегда false → второй вызов шлёт дубль, красный.
func TestSetPermitAllACL_ForeignRuleOnly_PermitOnce(t *testing.T) {
	foreign := map[string]string{"v4": "permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0", "v6": "permit ipv6 2001:db8::/32 ::/0"}
	for _, f := range []aclFamily{aclV4, aclV6} {
		t.Run(f.name, func(t *testing.T) {
			cmds, poster := newACLTestCommandsRC(nil)
			fg := rcStore(cmds, append(rcIface(f, true), f.header, "    "+foreign[f.name], "    auto-delete"))
			if err := f.set(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("постановка: %v", err)
			}
			if want := []string{f.permitCmd(), f.bindCmd(), f.autoDeleteCmd()}; !slices.Equal(poster.parses, want) {
				t.Fatalf("parses = %v, ждали %v", poster.parses, want)
			}
			setRCLines(fg, append(rcIface(f, true), f.header, "    "+foreign[f.name], "    "+f.rule, "    auto-delete"))
			poster.parses = nil
			if err := f.set(cmds, confirmed(t, "OpkgTun15")); err != nil {
				t.Fatalf("повтор: %v", err)
			}
			if want := []string{f.bindCmd(), f.autoDeleteCmd()}; !slices.Equal(poster.parses, want) {
				t.Fatalf("повтор: parses = %v, ждали %v", poster.parses, want)
			}
		})
	}
}

// SetPermitAllACLs — оба семейства по одному чтению; без v6 — только v4.
// Мутация: чтение на каждое семейство → 2, красный; withV6 игнорируется →
// v6-команды без v6, красный.
func TestSetPermitAllACLs_OneFetch(t *testing.T) {
	for _, withV6 := range []bool{true, false} {
		cmds, poster := newACLTestCommandsRC(nil)
		fg := rcStore(cmds, nil)
		if err := cmds.SetPermitAllACLs(context.Background(), confirmed(t, "OpkgTun15"), withV6); err != nil {
			t.Fatalf("withV6=%v: %v", withV6, err)
		}
		if n := fg.Calls("/show/running-config"); n != 1 {
			t.Errorf("withV6=%v: чтений running-config = %d, ждали 1", withV6, n)
		}
		want := []string{aclV4.permitCmd(), aclV4.bindCmd(), aclV4.autoDeleteCmd()}
		if withV6 {
			want = append(want, aclV6.permitCmd(), aclV6.bindCmd(), aclV6.autoDeleteCmd())
		}
		if !slices.Equal(poster.parses, want) {
			t.Errorf("withV6=%v: parses = %v, ждали %v", withV6, poster.parses, want)
		}
	}
}
