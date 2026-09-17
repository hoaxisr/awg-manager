// Package netres — netfilter-ресурсы прокси-ролей. Правило описано ОДИН раз
// (Rule); вставка, проверка, снос и строка netfilter.d-хука — рендеры одного
// значения. История, зачем: в 2.17.0 формы вставки и сноса разъехались, и
// снос перестал удалять что-либо (entware_nat_linux.go:417-422); хук был
// четвёртым независимым носителем той же формы.
package netres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// IPT — исполнитель iptables. Прод — internal/sys/iptables (Run + RunOutput);
// второго адаптера нет и не планируется (кандидат №7): интерфейс существует
// ради модели таблиц в тестах, и продакшн не несёт ни одной ветки под неё.
type IPT interface {
	Run(ctx context.Context, args ...string) error
	// Output — вывод `-S <chain>`: нужен усыновлению-по-метке (правила
	// прежних запусков демона в ведомость разности не попадают — их находит
	// только листинг живой цепочки).
	Output(ctx context.Context, args ...string) (string, error)
}

// Rule — одно правило. Spec — match+target без -t/-C/-I/-D.
type Rule struct {
	Table string // "" = filter
	Chain string
	Pos   int // 0 = append (-A), иначе -I Chain Pos
	Spec  []string
}

func (r Rule) table() string {
	if r.Table == "" {
		return "filter"
	}
	return r.Table
}

func (r Rule) prefix() []string {
	if r.Table == "" || r.Table == "filter" {
		return nil
	}
	return []string{"-t", r.Table}
}

// CheckArgs — форма проверки (-C).
func (r Rule) CheckArgs() []string {
	return append(append(r.prefix(), "-C", r.Chain), r.Spec...)
}

// InsertArgs — форма вставки.
func (r Rule) InsertArgs() []string {
	if r.Pos > 0 {
		return append(append(r.prefix(), "-I", r.Chain, strconv.Itoa(r.Pos)), r.Spec...)
	}
	return append(append(r.prefix(), "-A", r.Chain), r.Spec...)
}

// DeleteArgs — форма сноса.
func (r Rule) DeleteArgs() []string {
	return append(append(r.prefix(), "-D", r.Chain), r.Spec...)
}

// Key — каноническая форма правила: по ней считается разность прежнего и
// нового желаемого. Позиция вставки в ключ не входит: правило с той же
// формой на другой позиции — то же правило.
func (r Rule) Key() string {
	return r.table() + "|" + r.Chain + "|" + strings.Join(canonSpec(r.Spec), " ")
}

// canonSpec — форма для СРАВНЕНИЯ живого правила с желаемым. Только для
// сравнения: в `-D` обязана уходить строка, которую напечатал `iptables -S`,
// иначе снятие не найдёт правило.
//
// Зачем. markedOrphans берёт помеченные правила из живой цепочки, сравнивает с
// желаемым ТЕКСТОМ и всё лишнее СНОСИТ. Вердикт разрушительный, а iptables
// печатает не ровно то, что ему дали:
//
//   - `-p udp --dport 53` печатается как `-p udp -m udp --dport 53` — модуль
//     матча, который iptables подгружает сам;
//   - голый адрес канонизируется в `/32` (`/128` для v6).
//
// Сегодня не рвётся по случайности: единственное правило с `--comment` —
// MASQUERADE, у него нет `-p`, а адрес уже в форме CIDR. Добавьте `--comment`
// любому правилу с `-p` — и оно попадёт в выборку по метке, не совпадёт с
// желаемым, будет объявлено сиротой и снесено. Каждый раунд: снесли → ensure
// поставил заново → снесли (F347).
//
// Лечим ОТБРАСЫВАНИЕМ, а не предсказанием формы прошивки: обе стороны гоним
// через одну функцию, и она убирает различающиеся написания независимо от
// того, какая из сторон их несёт. Поэтому правка не зависит от того, что
// именно печатает конкретная сборка iptables, и не требует стендовых данных.
func canonSpec(spec []string) []string {
	out := make([]string, 0, len(spec))
	for i := 0; i < len(spec); i++ {
		tok := spec[i]
		// `-m udp` сразу после `-p udp` — неявная загрузка модуля матча,
		// смысла не несёт. `-m comment` под правило не подпадает: перед ним
		// стоит не `-p comment`.
		if tok == "-m" && i+1 < len(spec) && i >= 2 && spec[i-2] == "-p" && spec[i-1] == spec[i+1] {
			i++
			continue
		}
		// Одиночный адрес: `10.0.0.1` и `10.0.0.1/32` — одно и то же.
		if (tok == "-s" || tok == "-d" || tok == "--source" || tok == "--destination") && i+1 < len(spec) {
			out = append(out, tok, strings.TrimSuffix(strings.TrimSuffix(spec[i+1], "/32"), "/128"))
			i++
			continue
		}
		out = append(out, tok)
	}
	return out
}

// CommentTag — значение `--comment` правила; пусто, если метки нет.
func (r Rule) CommentTag() string {
	for i, tok := range r.Spec {
		if tok == "--comment" && i+1 < len(r.Spec) {
			return r.Spec[i+1]
		}
	}
	return ""
}

// listMarked перечисляет ЖИВЫЕ правила цепочки с меткой comment — включая
// поставленные прежними запусками демона. Разбор вывода `-S`: строки вида
// `-A CHAIN <spec>`; метка без пробелов, поэтому Fields достаточно.
func listMarked(ctx context.Context, ipt IPT, table, chain, comment string) ([]Rule, error) {
	out, err := ipt.Output(ctx, "-t", table, "-S", chain)
	if err != nil {
		return nil, err
	}
	var rules []Rule
	prefix := "-A " + chain + " "
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		spec := normalizeSpec(strings.Fields(strings.TrimPrefix(line, prefix)))
		rule := Rule{Table: table, Chain: chain, Spec: spec}
		if rule.CommentTag() == comment {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

// normalizeSpec приводит токены строки `-S` к той форме, которой мы правило
// СТАВИМ. Сегодня различие ровно одно: часть сборок iptables печатает значение
// `--comment` в кавычках (`--comment "AWGM_WDTT"`), часть — голым словом; на
// прошивке 4.3.8 mips (прогон 2026-08-18) кавычек нет, кавыченная форма
// зафиксирована в этом же репозитории для sb-router
// (internal/singbox/router/iptables.go:1328 и тест, запрещающий quoted-only
// матч). Кавычки — артефакт печати, не часть значения, и снимать их надо
// именно при разборе, а не при сравнении метки: тот же токен идёт в Rule.Key
// (иначе своё живое правило опознаётся сиротой — churn каждые 15 с) и в
// DeleteArgs (иначе `-D` с кавычками не находит правило).
func normalizeSpec(spec []string) []string {
	for i, tok := range spec {
		if tok == "--comment" && i+1 < len(spec) {
			spec[i+1] = strings.Trim(spec[i+1], `"`)
		}
	}
	return spec
}

// HookLine — строка netfilter.d-хука: `run -C … || run -I …`. Кавычки вокруг
// имени интерфейса в Spec расставляет построитель (hookQuote): хук и Go-код
// обязаны ставить одно и то же правило.
func (r Rule) HookLine() string {
	check := strings.Join(r.CheckArgs(), " ")
	insert := strings.Join(r.InsertArgs(), " ")
	return "run " + hookQuoteIfaces(check) + " || run " + hookQuoteIfaces(insert)
}

// hookQuoteIfaces берёт в кавычки аргументы после -i/-o — та же форма, что у
// старого генератора (entware_nat_linux.go:200: %q вокруг имени).
func hookQuoteIfaces(line string) string {
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "-i" || fields[i] == "-o" {
			if !strings.HasPrefix(fields[i+1], "\"") {
				fields[i+1] = fmt.Sprintf("%q", fields[i+1])
			}
		}
	}
	return strings.Join(fields, " ")
}

// builtinChains — встроенные цепочки iptables. Всё прочее — наша собственная
// цепочка, а в несуществующую цепочку правило не вставить: и `-C`, и `-I`
// вернут ошибку. Поэтому тот, кто восстанавливает такие правила (хук
// netfilter.d), обязан сперва её создать.
var builtinChains = map[string]bool{
	"INPUT": true, "OUTPUT": true, "FORWARD": true,
	"PREROUTING": true, "POSTROUTING": true,
}

// IsCustomChain — правило адресовано НЕ встроенной цепочке.
func (r Rule) IsCustomChain() bool { return !builtinChains[r.Chain] }

// Group — группа правил с общим guard-интерфейсом.
type Group struct {
	Guard string // имя интерфейса; пусто — без guard
	Rules []Rule
}

// present — все ли правила группы стоят.
func (g Group) present(ctx context.Context, ipt IPT) (all bool) {
	all = true
	for _, r := range g.Rules {
		if ipt.Run(ctx, r.CheckArgs()...) != nil {
			all = false
		}
	}
	return all
}

// ensure приводит группу.
func (g Group) ensure(ctx context.Context, ipt IPT) error {
	if g.present(ctx, ipt) {
		return nil
	}
	// Вставка на позицию 1 — в обратном порядке декларации, чтобы итоговый
	// порядок в цепочке совпал с порядком Rules.
	for i := len(g.Rules) - 1; i >= 0; i-- {
		r := g.Rules[i]
		if ipt.Run(ctx, r.CheckArgs()...) == nil {
			continue
		}
		if err := ipt.Run(ctx, r.InsertArgs()...); err != nil {
			return fmt.Errorf("%s %s: %w", r.table(), r.Chain, err)
		}
	}
	return nil
}
