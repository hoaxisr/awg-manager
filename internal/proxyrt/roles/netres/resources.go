package netres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// ruleRecheck — подстраховочная сверка: правила вычищает reconcile sing-box и
// перезапись таблиц движком ndm. Это ЕДИНСТВЕННЫЙ волатильный класс ресурсов
// (спека §3). Период задаёт компромисс «сколько правило лежит стёртым» против
// «сколько exec'ов на тик»; 15 с взяты у прежнего NAT-ресинка и замером не
// подтверждены — менять только по измерению на железе.
const ruleRecheck = 15 * time.Second

// RuleSet — ресурс «набор групп правил приведён». Общий для nat_rules и
// forward_rules: различие — данными (какие группы), не кодом.
// GroupProvider выдаёт желаемые группы в момент наблюдения. Провайдер, а не
// значение: internet-only-форма MASQUERADE требует kernel-имени WAN, а его
// разрешение — I/O, которому не место в сборке декларации.
type GroupProvider func(ctx context.Context) ([]Group, error)

// StaticGroups — провайдер константного набора.
func StaticGroups(groups []Group) GroupProvider {
	return func(context.Context) ([]Group, error) { return groups, nil }
}

type RuleSet struct {
	id       proxyrt.ResourceID
	ipt      IPT
	provider GroupProvider
	// last — группы последнего наблюдения: по ним применяется ensure.
	last []Group
	// doomed — ведомость на снос: правила прежних желаемых, которых нет в
	// текущем (разность по Rule.Key, G1). Не только «опустело»: смена
	// full→internet-only, WAN, интерфейса или RelayMode обязана сносить
	// прежние формы — иначе класс H1 (PR #697) и утечка правил 2.17.0.
	doomed map[string]Rule
	// reaped — ключи doomed-правил, снос которых ДОВЕДЁН до конца. Защёлка
	// для Doom: он зовётся из декларации роли каждый проход, а снятое правило
	// прежней версии обратно не появляется. Без защёлки ведомость воскресала
	// бы вечно, и Observe держал бы по `iptables -C` на правило каждый тик —
	// exec-churn на роутере (PR #734).
	//
	// Множество общее по пространству ключей (Rule.Key), но консультирует его
	// ТОЛЬКО Doom. Поэтому правило, ушедшее из желаемого и сметённое, а потом
	// снова ставшее желаемым и снова ушедшее, не застревает: путь разности
	// желаемых кладёт его в doomed сам, минуя латч. Плата — обратная: Doom
	// формы, СОВПАВШЕЙ с однажды сметённой, становится no-op до конца жизни
	// процесса.
	reaped map[string]bool
	// adopt — области усыновления, НЕ зависящие от текущего желаемого.
	// Владение принадлежит метке: что мы когда-то поставили со своей меткой,
	// то обязаны и снести — даже когда сейчас не хотим там ничего. Без этого
	// «нечего хотеть» читается как «нечего убирать»: у выключенного инстанса
	// и у режима без маскарада помеченных правил в желаемом нет, область
	// сканирования пуста, и правило прошлого запуска демона живёт дальше.
	adopt []adoptScope
}

// adoptScope — где искать чужие правила с нашей меткой.
type adoptScope struct{ table, chain, tag string }

// AdoptMarked объявляет постоянную область усыновления. Зовётся при сборке
// роли, по одному разу на (table, chain, метка).
func (r *RuleSet) AdoptMarked(table, chain, tag string) {
	r.adopt = append(r.adopt, adoptScope{table: table, chain: chain, tag: tag})
}

func NewRuleSet(id proxyrt.ResourceID, ipt IPT) *RuleSet {
	return &RuleSet{id: id, ipt: ipt, provider: StaticGroups(nil),
		doomed: map[string]Rule{}, reaped: map[string]bool{}}
}

// SetDesired: провайдер, дающий пустой набор, означает «правил быть не
// должно» — правила прошлого желаемого уходят в ведомость на снос.
func (r *RuleSet) SetDesired(provider GroupProvider) {
	if provider == nil {
		provider = StaticGroups(nil)
	}
	r.provider = provider
}

// Doom кладёт правила в ведомость на снос БЕЗ желаемого: форму, которую мы
// больше не ставим, но обязаны убрать. Разность желаемых её не даст (в
// желаемом её не было ни разу за этот запуск), усыновление-по-метке не
// увидит (метки правило не несёт) — остаётся назвать её адресно.
//
// Уже снесённое правило Doom НЕ воскрешает: зовут его из декларации роли, то
// есть каждый проход, а снос доводится один раз.
func (r *RuleSet) Doom(rules ...Rule) {
	for _, rule := range rules {
		if r.reaped[rule.Key()] {
			continue
		}
		r.doomed[rule.Key()] = rule
	}
}

func (r *RuleSet) ID() proxyrt.ResourceID { return r.id }

func (r *RuleSet) Observe(ctx context.Context) (proxyrt.Observation, error) {
	groups, err := r.provider(ctx)
	if err != nil {
		// Желаемое не собралось (WAN не разрешён и т.п.) — «не смогли
		// посмотреть», шагов не будет. Первичный гейт internet-only без WAN —
		// Validate конфига (задача 1): сюда этот случай доезжает только при
		// дрейфе конфига между прогонами.
		return proxyrt.Observation{}, err
	}
	// Разность прогонов: правило прежнего желаемого, которого нет в новом,
	// уходит в ведомость на снос (по канонической форме Rule.Key).
	current := map[string]bool{}
	for _, g := range groups {
		for _, rule := range g.Rules {
			current[rule.Key()] = true
		}
	}
	for _, g := range r.last {
		for _, rule := range g.Rules {
			if !current[rule.Key()] {
				r.doomed[rule.Key()] = rule
			}
		}
	}
	// Правило, вернувшееся в желаемое, из ведомости выходит.
	for key := range r.doomed {
		if current[key] {
			delete(r.doomed, key)
		}
	}
	r.last = groups
	missing := 0
	for _, g := range groups {
		all := g.present(ctx, r.ipt)
		if !all {
			missing++
		}
	}
	stale := 0
	for _, rule := range r.doomed {
		if r.ipt.Run(ctx, rule.CheckArgs()...) == nil {
			stale++
		}
	}
	// Усыновление-по-метке (I-1): помеченные правила прежних запусков демона
	// в doomed не попадают — их видит только листинг живой цепочки. Всё
	// помеченное сверх желаемого — тоже stale.
	orphans, err := r.markedOrphans(ctx, current)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	stale += len(orphans)
	return proxyrt.Observation{
		Known:  true,
		Exists: missing == 0 && stale == 0,
		Attrs: map[string]string{
			"missing": strconv.Itoa(missing),
			"stale":   strconv.Itoa(stale),
		},
	}, nil
}

// markedOrphans — помеченные правила, живые в ядре сверх текущего желаемого.
// Область поиска — объединение постоянных областей (AdoptMarked) и тех, где
// помеченные правила есть в текущем желаемом.
func (r *RuleSet) markedOrphans(ctx context.Context, current map[string]bool) ([]Rule, error) {
	seen := map[string]bool{}
	var orphans []Rule
	scan := func(table, chain, tag string) error {
		if tag == "" {
			return nil
		}
		scope := table + "|" + chain + "|" + tag
		if seen[scope] {
			return nil
		}
		seen[scope] = true
		live, err := listMarked(ctx, r.ipt, table, chain, tag)
		if err != nil {
			return err
		}
		for _, l := range live {
			if !current[l.Key()] {
				orphans = append(orphans, l)
			}
		}
		return nil
	}
	for _, a := range r.adopt {
		if err := scan(a.table, a.chain, a.tag); err != nil {
			return nil, err
		}
	}
	for _, g := range r.last {
		for _, rule := range g.Rules {
			if err := scan(rule.table(), rule.Chain, rule.CommentTag()); err != nil {
				return nil, err
			}
		}
	}
	return orphans, nil
}

func (r *RuleSet) Plan(obs proxyrt.Observation) []proxyrt.Step {
	var steps []proxyrt.Step
	if obs.Attrs["stale"] != "0" {
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: "sweep",
			Reason: "правила прежнего желаемого подлежат сносу"})
	}
	if obs.Attrs["missing"] != "0" {
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: "ensure",
			Args: map[string]string{"missing": obs.Attrs["missing"]}, Reason: "правила отсутствуют"})
	}
	return steps
}

func (r *RuleSet) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "ensure":
		for _, g := range r.last {
			if err := g.ensure(ctx, r.ipt); err != nil {
				return err
			}
		}
		return nil
	case "sweep":
		// M-2: неудавшийся снос НЕ выбрасывает правило из ведомости — иначе
		// оно потеряно навсегда (разность желаемых его больше не даст).
		var firstErr error
		for key, rule := range r.doomed {
			gone, err := r.deleteAll(ctx, rule)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if gone {
				delete(r.doomed, key)
				r.reaped[key] = true
			}
		}
		if firstErr != nil {
			return firstErr
		}
		// Усыновлённые сироты по метке (I-1): пересчитываются каждый Observe,
		// сносятся здесь же.
		current := map[string]bool{}
		for _, g := range r.last {
			for _, rule := range g.Rules {
				current[rule.Key()] = true
			}
		}
		orphans, err := r.markedOrphans(ctx, current)
		if err != nil {
			return err
		}
		for _, rule := range orphans {
			if err := r.ipt.Run(ctx, rule.DeleteArgs()...); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

// sweepPasses — потолок проходов сноса ОДНОГО правила за шаг. Смысл числа —
// ограничить цикл: копий правила бывает больше одной, а `iptables -D` снимает
// по одной за вызов. Пять — с запасом над реально виденными дублями (их
// единицы), точной величины за числом нет.
const sweepPasses = 5

// deleteAll сносит ВСЕ копии правила. `iptables -D` снимает ровно одну, а
// копий бывает больше (дубль от старого кода, второй вставки хука или
// прежнего запуска демона): один снос и выброс ключа оставлял вторую копию
// навсегда — разность желаемых её больше не даёт (I-3).
//
// gone=true возвращается только когда правила в цепочке достоверно нет. Отказ
// `-C`, из которого «правила нет» НЕ следует (занят xtables-lock, движок ndm
// переписывает таблицы, exec не запустился), оставляет правило в ведомости до
// следующего прохода — иначе оно теряется навсегда так же, как теряло M-2 на
// отказе `-D` (I-4).
func (r *RuleSet) deleteAll(ctx context.Context, rule Rule) (bool, error) {
	for pass := 0; pass < sweepPasses; pass++ {
		if err := r.ipt.Run(ctx, rule.CheckArgs()...); err != nil {
			return ruleAbsent(err), nil
		}
		if err := r.ipt.Run(ctx, rule.DeleteArgs()...); err != nil {
			return false, err
		}
	}
	// Потолок исчерпан — правило остаётся в ведомости до следующего sweep.
	return false, nil
}

// ruleAbsent отличает «такого правила нет» от отказа, из которого этого не
// следует. Кода возврата в ошибке нет: прод-исполнитель отдаёт её строкой
// (sys/iptables.Run → exec.FormatError со stderr), поэтому судим по тексту
// самого iptables. Всё неопознанное считается транзиентным: цена ошибки
// несимметрична — лишний проход дешевле правила, потерянного из ведомости.
func ruleAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Первое — ответ `-C` на несуществующее правило, второе — на
	// несуществующую цепочку или незагруженный матч. Сносить нечего в обоих.
	return strings.Contains(msg, "does a matching rule exist") ||
		strings.Contains(msg, "no chain/target/match by that name")
}

func (r *RuleSet) RecheckAfter() time.Duration {
	if len(r.last) == 0 && len(r.doomed) == 0 {
		return 0
	}
	return ruleRecheck
}

// MSSClamp — clamp в своей цепочке + jump из mangle/FORWARD.
//
// Ведомости-разности у ресурса нет (CIDR — константа, единственный переход —
// enabled→disabled): при опустевшем желаемом цепочка awgm_wdtt_mangle и jump
// остаются до первой перезаписи таблиц движком ndm — объявленный residual
// (M-5 ревью-3), окончательно закрывается стартовой уборкой плана 5.
type MSSClamp struct {
	id    proxyrt.ResourceID
	ipt   IPT
	cidrs []string
}

func NewMSSClamp(id proxyrt.ResourceID, ipt IPT) *MSSClamp {
	return &MSSClamp{id: id, ipt: ipt}
}

func (m *MSSClamp) SetDesired(cidrs []string) { m.cidrs = cidrs }

func (m *MSSClamp) ID() proxyrt.ResourceID { return m.id }

func (m *MSSClamp) jump() Rule {
	return Rule{Table: "mangle", Chain: "FORWARD", Pos: 1, Spec: []string{"-j", MSSChain}}
}

func (m *MSSClamp) Observe(ctx context.Context) (proxyrt.Observation, error) {
	if len(m.cidrs) == 0 {
		return proxyrt.Observation{Known: true, Exists: true, Detail: "clamp не нужен"}, nil
	}
	ok := m.ipt.Run(ctx, m.jump().CheckArgs()...) == nil
	if ok {
		for _, r := range MSSRules(m.cidrs) {
			if m.ipt.Run(ctx, r.CheckArgs()...) != nil {
				ok = false
				break
			}
		}
	}
	return proxyrt.Observation{Known: true, Exists: ok}, nil
}

func (m *MSSClamp) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	return []proxyrt.Step{{Resource: m.id, Op: "ensure", Reason: "clamp не собран"}}
}

func (m *MSSClamp) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "ensure" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	_ = m.ipt.Run(ctx, "-t", "mangle", "-N", MSSChain)
	_ = m.ipt.Run(ctx, "-t", "mangle", "-F", MSSChain)
	for _, r := range MSSRules(m.cidrs) {
		if err := m.ipt.Run(ctx, r.InsertArgs()...); err != nil {
			return err
		}
	}
	// Дубли jump снимаются до трёх раз: `-D` снимает по одному, а вторая
	// копия появляется от повторной вставки хука. Три — с запасом, точной
	// величины за числом нет.
	for i := 0; i < 3; i++ {
		_ = m.ipt.Run(ctx, m.jump().DeleteArgs()...)
	}
	return m.ipt.Run(ctx, m.jump().InsertArgs()...)
}

func (m *MSSClamp) RecheckAfter() time.Duration {
	if len(m.cidrs) == 0 {
		return 0
	}
	return ruleRecheck
}

// PortSpec — WAN-порт INPUT.
type PortSpec struct {
	Port  int
	Proto string
}

// FW — исполнитель INPUT-правил. Прод — internal/listenfirewall: у него своя
// метка AWGM_PROXY_LISTEN, свой хук 62-awgm-listen-ports.sh и ДЕКЛАРАТИВНЫЙ
// Reconcile(desired), снимающий stale по listManaged (firewall_linux.go:80) —
// то есть ведомость, переживающая рестарт демона ПО ПОСТРОЕНИЮ (I-1):
// протухший порт находится в живых правилах, а не в памяти процесса.
// Managed — листинг наших живых правил; прод-адаптеру плана 5 нужна
// экспорт-обёртка над неэкспортированным listManaged (firewall_linux.go:133).
type FW interface {
	Managed(ctx context.Context) ([]PortSpec, error)
	Reconcile(ctx context.Context, desired []PortSpec) error
}

// InputPort — ресурс input_port. Желаемое сверяется с ЖИВЫМИ правилами
// (Managed), приведение — Reconcile: смена WAN-порта закрывает прежний, в том
// числе открытый ПРЕЖНИМ запуском демона — без этого его вечно
// восстанавливал бы собственный хук listenfirewall (самоувековечивание).
type InputPort struct {
	id   proxyrt.ResourceID
	fw   FW
	want []PortSpec
}

func portKey(s PortSpec) string { return s.Proto + "/" + strconv.Itoa(s.Port) }

func NewInputPort(id proxyrt.ResourceID, fw FW) *InputPort {
	return &InputPort{id: id, fw: fw}
}

func (p *InputPort) SetDesired(specs []PortSpec) { p.want = specs }

func (p *InputPort) ID() proxyrt.ResourceID { return p.id }

func (p *InputPort) Observe(ctx context.Context) (proxyrt.Observation, error) {
	managed, err := p.fw.Managed(ctx)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	have := map[string]bool{}
	for _, s := range managed {
		have[portKey(s)] = true
	}
	wantKeys := map[string]bool{}
	missing := 0
	for _, s := range p.want {
		wantKeys[portKey(s)] = true
		if !have[portKey(s)] {
			missing++
		}
	}
	stale := 0
	for _, s := range managed {
		if !wantKeys[portKey(s)] {
			stale++
		}
	}
	return proxyrt.Observation{Known: true, Exists: missing == 0 && stale == 0,
		Attrs: map[string]string{"missing": strconv.Itoa(missing), "stale": strconv.Itoa(stale)}}, nil
}

func (p *InputPort) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	return []proxyrt.Step{{Resource: p.id, Op: "ensure", Reason: "INPUT-порты не приведены"}}
}

func (p *InputPort) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "ensure" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	return p.fw.Reconcile(ctx, p.want)
}

func (p *InputPort) RecheckAfter() time.Duration {
	if len(p.want) == 0 {
		return 0
	}
	return ruleRecheck
}
