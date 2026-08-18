package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// NAT-режим сегмента (он же PriorMode в storage.PolicyTunNATSegment):
// dynamic — `ip nat <Seg>` (маскарад), static — `ip static <Seg> <WAN>`
// (SNAT только в этот выход), none — трансляции нет вовсе.
const (
	natModeDynamic = "dynamic"
	natModeStatic  = "static"
	natModeNone    = "none"
)

// NATSegmentInfo — строка предпоказа source-preserve: что за сегмент и в каком
// он NAT-режиме сейчас. StaticWAN заполнен только для static.
//
// Label и Subnet — для человека: системные имена (`Home`, `Wireguard1`) он
// видит только в веб-морде роутера, а выбирать ему предстоит то, у чего
// меняется способ выхода в интернет. Пустые, когда NDMS описания не дал или
// адресации у сегмента нет.
// Masq — СЫРОЙ признак `ip nat <Seg>`, не выводимый из Mode: `ip static`
// перекрывает динамику в Mode (так это показывает пользователю CLI-мануал),
// но сама строка `ip nat` при этом остаётся в конфиге и продолжает маскарадить
// путь в туннель. Судить о том, снимать ли маскарад, можно только по Masq —
// иначе сегмент с обеими строками сверка считала бы вечно неприменённым и
// дёргала бы RCI каждый тик. Наружу не отдаётся: предпоказу хватает Mode.
type NATSegmentInfo struct {
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	StaticWAN string `json:"staticWan,omitempty"`
	Label     string `json:"label,omitempty"`
	Subnet    string `json:"subnet,omitempty"`
	Masq      bool   `json:"-"`
}

// NATEgress — выход роутера, на котором подмена адреса сохранится: туда встанет
// `ip static`. Label пуст, когда NDMS не дал описания.
type NATEgress struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
}

// NATPreview — всё, что экран «Сохранять адреса клиентов» показывает до
// включения: сами сегменты и ВЫХОДЫ, на которых подмена сохранится. Без второй
// половины пользователь видит только «какие сети выбрать» и не видит, между чем
// и чем подмена снимается.
//
// Egresses пуст, когда выходы определить нельзя (running-config не прочитался,
// а дефолт уже припаркован на нашем tun).
type NATPreview struct {
	Segments []NATSegmentInfo
	Egresses []NATEgress
}

// SegmentInfo — то, что NDMS знает о сегменте помимо его имени.
type SegmentInfo struct {
	Label   string // description NDMS; пусто, если не задано
	Address string // адрес интерфейса, напр. "192.168.1.1"
	Mask    string // маска, напр. "255.255.255.0"
}

// SegmentDetails отдаёт описание и адресацию сегмента по NDMS-имени.
// Optional — nil означает «показываем системные имена, как раньше».
type SegmentDetails interface {
	SegmentInfo(ctx context.Context, ndmsName string) (SegmentInfo, error)
}

// segmentSubnet сводит адрес и маску интерфейса в подсеть ("192.168.1.0/24").
// Пустая строка при любой неполноте: подпись «сеть такая-то» обязана быть
// верной либо отсутствовать.
func segmentSubnet(address, mask string) string {
	ip := net.ParseIP(address).To4()
	m := net.ParseIP(mask).To4()
	if ip == nil || m == nil {
		return ""
	}
	ipnet := net.IPNet{IP: ip.Mask(net.IPMask(m)), Mask: net.IPMask(m)}
	return ipnet.String()
}

// DefaultGatewayResolver отдаёт NDMS-имя интерфейса с дефолтным маршрутом —
// единственный источник WAN-цели для static-NAT. Optional — nil в тестах;
// wired в cmd/awg-manager на *query.RouteStore.
//
// Резолва «закреплённый WAN (kernel-имя) → NDMS-имя» сознательно НЕТ (YAGNI):
// пользователь может закрепить WAN только для sing-box (SingboxRouterSettings.
// WANInterface хранит kernel-имя), а NDMS-цель static-NAT берётся из живого
// дефолта. Если понадобится — это отдельный резолвер поверх InterfaceStore.
type DefaultGatewayResolver interface {
	GetDefaultGatewayInterface(ctx context.Context) (string, error)
}

// PolicyTunNATPreview — предпоказ «что будет изменено» для тумблера
// «Сохранять адреса клиентов»: сегменты роутера с их текущим NAT-режимом.
// Пользователь снимает галочки с сегментов, которые трогать не надо.
func (s *ServiceImpl) PolicyTunNATPreview(ctx context.Context) (NATPreview, error) {
	if s.deps.NATState == nil {
		return NATPreview{}, fmt.Errorf("policy-tun: NAT state reader not wired")
	}
	segs, err := s.scanSegmentNAT(ctx)
	if err != nil {
		return NATPreview{}, err
	}
	out := NATPreview{Segments: s.dropEgressSegments(ctx, segs)}

	// Выходы, на которые встанет static — те же, что применит apply. Best-effort:
	// молчащий RCI оставит список пустым, и экран назовёт выход обобщённо, а не
	// соврёт числом или именем.
	if targets, terr := s.policyTunSNATTargets(ctx); terr == nil {
		for _, name := range targets {
			e := NATEgress{Name: name}
			if s.deps.Segments != nil {
				if info, ierr := s.deps.Segments.SegmentInfo(ctx, name); ierr == nil {
					e.Label = info.Label
				}
			}
			out.Egresses = append(out.Egresses, e)
		}
	}
	return out, nil
}

// scanSegmentNAT сводит два структурированных списка NDMS (`/show/rc/ip/nat` и
// `/show/rc/ip/static`) в режимы сегментов. Текстовый парсинг running-config
// тут негоден: `ip static` бывает и порт-форвардингом с протоколом/портами.
//
// Отбрасываются: записи без interface (форма `ip nat <address> <mask>` задаёт
// источник подсетью), записи `ip static` без to-interface (порт-форвардинг, а
// не сегментный SNAT) и наши собственные OpkgTun (они — выход, не источник).
// `ip static` приоритетнее `ip nat` (CLI-мануал §3.83), поэтому статика
// перекрывает динамику по тому же сегменту.
func (s *ServiceImpl) scanSegmentNAT(ctx context.Context) ([]NATSegmentInfo, error) {
	dyn, err := s.deps.NATState.ListNAT(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ip nat: %w", err)
	}
	static, err := s.deps.NATState.ListStaticNAT(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ip static: %w", err)
	}

	var out []NATSegmentInfo
	idx := map[string]int{}
	for _, e := range dyn {
		if e.Interface == "" || isOpkgTunName(e.Interface) {
			continue
		}
		if _, ok := idx[e.Interface]; ok {
			continue
		}
		idx[e.Interface] = len(out)
		out = append(out, NATSegmentInfo{Name: e.Interface, Mode: natModeDynamic, Masq: true})
	}
	for _, e := range static {
		if e.Interface == "" || e.ToInterface == "" || isOpkgTunName(e.Interface) {
			continue
		}
		info := NATSegmentInfo{Name: e.Interface, Mode: natModeStatic, StaticWAN: e.ToInterface}
		if i, ok := idx[e.Interface]; ok {
			// Mode перекрываем, Masq — нет: строка `ip nat` никуда не делась.
			info.Masq = out[i].Masq
			out[i] = info
			continue
		}
		idx[e.Interface] = len(out)
		out = append(out, info)
	}
	s.labelSegments(ctx, out)
	return out, nil
}

// dropEgressSegments убирает из предпоказа интерфейсы с `ip global` — это
// ВЫХОДЫ наружу (WAN, клиентские VPN-туннели), а не сети клиентов. `ip nat
// PPPoE0` в конфиге роутера описывает трансляцию самого WAN, и снятие его нашей
// галкой — выстрел в интернет роутера; предлагать такое в списке «чьи адреса
// сохранить» нельзя.
//
// Только предпоказ: живой скан для apply остаётся полным, иначе сегмент,
// выбранный до этой правки, потерял бы своё исходное состояние в записи.
// Running-config молчит → фильтровать нечем, показываем как раньше.
func (s *ServiceImpl) dropEgressSegments(ctx context.Context, segs []NATSegmentInfo) []NATSegmentInfo {
	if s.deps.RunningConfig == nil {
		return segs
	}
	lines, err := s.deps.RunningConfig.Lines(ctx)
	if err != nil {
		return segs
	}
	egress := map[string]bool{}
	for _, name := range globalEgressInterfaces(lines) {
		egress[name] = true
	}
	if len(egress) == 0 {
		return segs
	}
	out := make([]NATSegmentInfo, 0, len(segs))
	for _, seg := range segs {
		if egress[seg.Name] {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// labelSegments дополняет строки предпоказа описанием и подсетью. Best-effort:
// отказ NDMS оставляет системные имена, но не валит предпоказ — без него опцию
// вообще не включить.
func (s *ServiceImpl) labelSegments(ctx context.Context, segs []NATSegmentInfo) {
	if s.deps.Segments == nil {
		return
	}
	for i := range segs {
		info, err := s.deps.Segments.SegmentInfo(ctx, segs[i].Name)
		if err != nil {
			s.appLog.Debug("policy-tun-nat", segs[i].Name, "segment info: "+err.Error())
			continue
		}
		segs[i].Label = info.Label
		segs[i].Subnet = segmentSubnet(info.Address, info.Mask)
	}
}

// isOpkgTunName сообщает, что имя принадлежит OpkgTun-семейству (наш policy-tun
// или fakeip-tun): такие интерфейсы в предпоказе сегментов не участвуют и
// WAN-целью static-NAT быть не могут.
func isOpkgTunName(name string) bool {
	return strings.HasPrefix(name, "OpkgTun")
}

// staticNATCoverage отдаёт живые `ip static` в виде «сегмент → множество
// выходов». Нужен сверке: наличие ЗАПИСИ о сегменте не означает, что static
// доехал до роутера (apply пишет запись до мутации, чтобы полуприменённое не
// пропало), поэтому судить о применённости можно только по живому конфигу.
func (s *ServiceImpl) staticNATCoverage(ctx context.Context) (map[string]map[string]bool, error) {
	live, err := s.deps.NATState.ListStaticNAT(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ip static: %w", err)
	}
	out := map[string]map[string]bool{}
	for _, e := range live {
		if e.Interface == "" || e.ToInterface == "" {
			continue
		}
		if out[e.Interface] == nil {
			out[e.Interface] = map[string]bool{}
		}
		out[e.Interface][e.ToInterface] = true
	}
	return out, nil
}

// policyTunNATApplied сообщает, что source-preserve доехал до КАЖДОГО желаемого
// сегмента. Пустой want — опция выключена: применять нечего, и «применено» тут
// сказать не о чем.
func policyTunNATApplied(want []string, recorded []storage.PolicyTunNATSegment) bool {
	if len(want) == 0 {
		return false
	}
	have := make(map[string]bool, len(recorded))
	for _, rec := range recorded {
		have[rec.Name] = true
	}
	for _, name := range want {
		if !have[name] {
			return false
		}
	}
	return true
}

// mergePolicyTunNATRecords дополняет prior записями recorded, НЕ затирая уже
// известные: первая запись о сегменте и есть истинное исходное состояние.
func mergePolicyTunNATRecords(prior, recorded []storage.PolicyTunNATSegment) []storage.PolicyTunNATSegment {
	out := append([]storage.PolicyTunNATSegment(nil), prior...)
	known := make(map[string]bool, len(out))
	for _, rec := range out {
		known[rec.Name] = true
	}
	for _, rec := range recorded {
		if known[rec.Name] {
			continue
		}
		known[rec.Name] = true
		out = append(out, rec)
	}
	return out
}

// applyPolicyTunSourcePreserve переводит выбранные сегменты на static-NAT в WAN,
// возвращая ЗАПИСАННОЕ исходное состояние каждого — teardown восстановит именно
// его, а не безусловный `ip nat` (пользователь мог сам держать static/no-nat).
//
// known — уже записанные исходные состояния (персист режима). Для сегмента из
// этого набора живой скан отдал бы НАШ static-NAT, поэтому запись НЕ
// переписывается: она старше и единственная знает, что было до нас.
//
// Ошибка любого шага фейлит apply целиком: вызывающий enable откатывает работу
// push-rollback'ом, а полуприменённый source-preserve хуже невключённого.
// Вместе с ошибкой возвращаются записи о сегментах, до которых apply дошёл, —
// вызывающий обязан сохранить/откатить их, иначе мутация останется на роутере
// невидимой для восстановления.
func (s *ServiceImpl) applyPolicyTunSourcePreserve(ctx context.Context, segs []string, known []storage.PolicyTunNATSegment) ([]storage.PolicyTunNATSegment, error) {
	if s.deps.SegmentNAT == nil || s.deps.NATState == nil || s.deps.DefaultGateway == nil {
		return nil, fmt.Errorf("policy-tun source-preserve: deps not wired")
	}
	targets, err := s.policyTunSNATTargets(ctx)
	if err != nil {
		return nil, err
	}
	scan, err := s.scanSegmentNAT(ctx)
	if err != nil {
		return nil, fmt.Errorf("policy-tun source-preserve: %w", err)
	}
	prior := map[string]NATSegmentInfo{}
	for _, info := range scan {
		prior[info.Name] = info
	}
	recordedBefore := map[string]storage.PolicyTunNATSegment{}
	for _, rec := range known {
		recordedBefore[rec.Name] = rec
	}

	recorded := make([]storage.PolicyTunNATSegment, 0, len(segs))
	for _, seg := range segs {
		mode := natModeNone
		info, ok := prior[seg]
		if ok {
			mode = info.Mode
		}
		if info.Masq {
			// Строка `ip nat` есть — значит снимать её нам, и восстанавливать
			// потом тоже её, даже если Mode перекрыт чужим `ip static`.
			// Известная неточность: собственный static такого сегмента вернуть
			// нечем, PriorStaticWAN при dynamic не читается.
			mode = natModeDynamic
		}
		// Запись ДО мутации: сегмент, у которого маскарад уже сняли, а static
		// доставить не успели, обязан быть виден восстановлению.
		if was, ok := recordedBefore[seg]; ok {
			// Запись старше живого скана — возвращаем её (в том числе в rollback
			// вызывающего, иначе откат вернул бы сегмент на НАШ static-NAT).
			recorded = append(recorded, was)
		} else {
			recorded = append(recorded, storage.PolicyTunNATSegment{
				Name:           seg,
				PriorMode:      mode,
				PriorStaticWAN: info.StaticWAN,
			})
		}
		if mode == natModeDynamic {
			// Снимаем маскарад ТОЛЬКО у динамических: у static его нет, а у
			// none снимать нечего.
			if err := s.deps.SegmentNAT.RemoveSegmentNAT(ctx, seg); err != nil {
				return recorded, fmt.Errorf("policy-tun source-preserve: no ip nat %s: %w", seg, err)
			}
		}
		// По записи на КАЖДЫЙ выход политики: SNAT нужен там, куда трафик
		// сегмента реально уходит, а не только на WAN по дефолтному маршруту.
		for _, target := range targets {
			if err := s.deps.SegmentNAT.SetStaticNAT(ctx, seg, target); err != nil {
				return recorded, fmt.Errorf("policy-tun source-preserve: ip static %s %s: %w", seg, target, err)
			}
		}
	}
	return recorded, nil
}

// policyTunSNATTargets отдаёт выходы, на которых сегментам нужен static-NAT:
// ВСЕ интерфейсы с `ip global`, кроме наших OpkgTun.
//
// Не один WAN по дефолтному маршруту: `ip static` — общероутерная настройка,
// правило SNAT вешается на выходной интерфейс и срабатывает для любого
// трафика сегмента, ушедшего в него. Сегмент, у которого сняли маскарад,
// теряет подмену адреса на КАЖДОМ выходе сразу, поэтому вернуть её надо на
// каждом — иначе к невзятому выходу трафик уйдёт с приватным адресом
// источника и не вернётся.
//
// Не выходы политики: устройства сегмента могут ходить и мимо неё, а состав
// политики меняется без всякого пересчёта static-NAT.
//
// Список пуст (running-config не прочитался, `ip global` нигде не нашёлся) →
// прежнее поведение: единственная цель — WAN по дефолту.
func (s *ServiceImpl) policyTunSNATTargets(ctx context.Context) ([]string, error) {
	var exits []string
	if s.deps.RunningConfig != nil {
		if lines, err := s.deps.RunningConfig.Lines(ctx); err == nil {
			for _, e := range globalEgressInterfaces(lines) {
				if !isOpkgTunName(e) {
					exits = append(exits, e)
				}
			}
		}
	}
	if len(exits) > 0 {
		return exits, nil
	}
	wan, err := s.resolvePolicyTunWAN(ctx)
	if err != nil {
		return nil, err
	}
	return []string{wan}, nil
}

// resolvePolicyTunWAN отдаёт NDMS-имя WAN для static-NAT по живому дефолту.
//
// ГОЧА (ключевая для порядка вызовов): в policy-tun NDMS-дефолт паркуется на
// наш же OpkgTun, и после парковки резолвер вернул бы именно его. SNAT сегмента
// В TUN — это ровно тот маскарад, от которого опция и спасает, поэтому такой
// ответ отвергается fail-closed, а enable зовёт apply ДО SetDefaultRoute.
func (s *ServiceImpl) resolvePolicyTunWAN(ctx context.Context) (string, error) {
	wan, err := s.deps.DefaultGateway.GetDefaultGatewayInterface(ctx)
	if err != nil {
		return "", fmt.Errorf("policy-tun source-preserve: resolve WAN: %w", err)
	}
	if wan == "" || isOpkgTunName(wan) {
		return "", fmt.Errorf("policy-tun source-preserve: WAN не определён (дефолтный маршрут ведёт на %q)", wan)
	}
	return wan, nil
}

// restorePolicyTunNAT возвращает сегментам записанное исходное состояние.
// Best-effort с агрегацией ошибок: teardown не прерывается на одном сегменте.
//
// Наш `ip static` снимается по ЖИВОМУ скану (to-interface из NDMS), а не по
// резолву WAN: к моменту teardown дефолт уже припаркован на tun, и угадывать
// цель нечем; к тому же WAN мог смениться с момента apply, и запись бы протухла.
func (s *ServiceImpl) restorePolicyTunNAT(ctx context.Context, recorded []storage.PolicyTunNATSegment) error {
	if len(recorded) == 0 {
		return nil
	}
	if s.deps.SegmentNAT == nil || s.deps.NATState == nil {
		return fmt.Errorf("policy-tun source-preserve: deps not wired")
	}
	live, err := s.deps.NATState.ListStaticNAT(ctx)
	if err != nil {
		return fmt.Errorf("policy-tun source-preserve restore: list ip static: %w", err)
	}
	wans := map[string][]string{}
	for _, e := range live {
		if e.Interface == "" || e.ToInterface == "" {
			continue
		}
		wans[e.Interface] = append(wans[e.Interface], e.ToInterface)
	}

	var errs []error
	for _, rec := range recorded {
		for _, wan := range wans[rec.Name] {
			if err := s.deps.SegmentNAT.RemoveStaticNAT(ctx, rec.Name, wan); err != nil {
				errs = append(errs, fmt.Errorf("no ip static %s %s: %w", rec.Name, wan, err))
			}
		}
		switch rec.PriorMode {
		case natModeDynamic:
			if err := s.deps.SegmentNAT.SetSegmentNAT(ctx, rec.Name); err != nil {
				errs = append(errs, fmt.Errorf("ip nat %s: %w", rec.Name, err))
			}
		case natModeStatic:
			if rec.PriorStaticWAN == "" {
				continue
			}
			if err := s.deps.SegmentNAT.SetStaticNAT(ctx, rec.Name, rec.PriorStaticWAN); err != nil {
				errs = append(errs, fmt.Errorf("ip static %s %s: %w", rec.Name, rec.PriorStaticWAN, err))
			}
		}
	}
	return errors.Join(errs...)
}

// restoreRevokedPolicyTunNAT снимает source-preserve с сегментов, которых
// больше нет в желаемом наборе: галку выключили целиком или убрали сегмент из
// списка. Без этого выключение опции вживую было бы инертным — роутер молча
// оставался бы на нашем static-NAT при выключенной в UI опции (UpdateSettings
// завершается Reconcile'ом, другого пути применения нет).
//
// Направление «снять» WAN-резолва не требует (см. restorePolicyTunNAT: наш
// `ip static` снимается по живому скану), поэтому работает и при дефолте,
// припаркованном на tun. Обратное направление — за reconcilePolicyTunNAT.
//
// Анти-churn: записи вычищаются из персиста ТОЛЬКО после успешного
// восстановления, и следующий тик уже не находит отозванных.
func (s *ServiceImpl) restoreRevokedPolicyTunNAT(ctx context.Context, sr storage.SingboxRouterSettings, st *storage.PolicyTunState, iface string) {
	if st == nil || len(st.NATSegments) == 0 || s.deps.SegmentNAT == nil || s.deps.NATState == nil {
		return
	}
	want := map[string]bool{}
	if sr.PolicyTunSourcePreserve {
		for _, seg := range sr.PolicyTunNATSegments {
			want[seg] = true
		}
	}
	var revoked []storage.PolicyTunNATSegment
	kept := make([]storage.PolicyTunNATSegment, 0, len(st.NATSegments))
	for _, rec := range st.NATSegments {
		if want[rec.Name] {
			kept = append(kept, rec)
			continue
		}
		revoked = append(revoked, rec)
	}
	if len(revoked) == 0 {
		return
	}
	if err := s.restorePolicyTunNAT(ctx, revoked); err != nil {
		// Записи НЕ снимаем: следующий тик повторит восстановление.
		s.appLog.Warn("policy-tun-reconcile", iface, "restore revoked segment NAT: "+err.Error())
		return
	}
	names := make([]string, 0, len(revoked))
	for _, rec := range revoked {
		names = append(names, rec.Name)
	}
	s.appLog.Info("policy-tun-reconcile", iface,
		"source-preserve снят с сегментов — исходный NAT восстановлен: "+strings.Join(names, ", "))
	if len(kept) == 0 {
		kept = nil
	}
	st.NATSegments = kept
	// В кэш уходит КОПИЯ: SetPolicyTunState кладёт сам указатель, а вызывающий
	// продолжает писать в st ниже по стеку (reconcilePolicyTunNAT). Опубликуй
	// мы st — эти записи шли бы уже в объект кэша, который параллельно маршалят
	// читатели без нашего лока.
	cp := *st
	if err := s.deps.Settings.SetPolicyTunState(&cp); err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, "persist nat segments: "+err.Error())
	}
}

// reconcilePolicyTunNAT доводит source-preserve до желаемого состояния, покрывая
// оба расхождения:
//
//   - НЕ ПРИМЕНЁН: сегмент есть в настройках, а записи apply о нём нет — галку
//     или сам сегмент добавили при работающем режиме. UpdateSettings ничего не
//     применяет, он завершается Reconcile'ом, и применение живёт здесь;
//   - ДРЕЙФ: сегмент с записью вернулся на динамический `ip nat` мимо нас
//     (сброс настроек NDMS, ручная правка), то есть sing-box снова видит
//     клиентов маскарадом.
//
// Записанные ранее исходные состояния не переписываем (иначе «пользователь
// держал none» превратилось бы в «держал dynamic») — за это отвечает параметр
// known у apply и mergePolicyTunNATRecords на выходе.
//
// Целями SNAT служат все `ip global` роутера кроме наших OpkgTun
// (policyTunSNATTargets), а они от дефолтного маршрута не зависят — поэтому
// применение вживую работает и при дефолте, уже припаркованном на tun. Резолв
// WAN по дефолту остаётся лишь запасным путём для молчащего running-config: там
// чинить нечем, и мы честно предупреждаем вместо SNAT в собственный tun.
func (s *ServiceImpl) reconcilePolicyTunNAT(ctx context.Context, sr storage.SingboxRouterSettings, st *storage.PolicyTunState, iface string) {
	if !sr.PolicyTunSourcePreserve || len(sr.PolicyTunNATSegments) == 0 ||
		s.deps.NATState == nil || s.deps.SegmentNAT == nil || st == nil {
		return
	}
	scan, err := s.scanSegmentNAT(ctx)
	if err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, "scan segment NAT: "+err.Error())
		return
	}
	// Сырой маскарад, а не Mode: сегмент с обеими строками (`ip nat` + чужой
	// `ip static`) в Mode выглядит статикой, но маскарад у него жив.
	masq := map[string]bool{}
	for _, info := range scan {
		masq[info.Name] = info.Masq
	}
	targets, err := s.policyTunSNATTargets(ctx)
	if err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, "выходы для static-NAT: "+err.Error())
		return
	}
	covered, err := s.staticNATCoverage(ctx)
	if err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, err.Error())
		return
	}
	// Судим по ЖИВОМУ состоянию, а не по наличию записи: запись пишется до
	// мутации, и после сбойного apply она есть, а маскарад уже снят и static
	// ещё не доставлен. Классификация по записи оставила бы такой сегмент без
	// трансляции навсегда — ни pending, ни drifted.
	needsApply := func(seg string) bool {
		if masq[seg] {
			return true // маскарад жив
		}
		for _, target := range targets {
			if !covered[seg][target] {
				return true // static доехал не до всех выходов
			}
		}
		return false
	}
	applied := map[string]bool{}
	for _, rec := range st.NATSegments {
		applied[rec.Name] = true
	}
	// Разделены только ради журнала: пользователю важно отличить «включили и
	// применилось» от «кто-то откатил наше применение». Дрейфом считаем лишь то,
	// что мы точно применяли: запись есть, наши static на месте — и поверх них
	// вернулся маскарад. Сегмент без наших static мы просто не доделали, и Warn
	// о чужой правке там был бы ложью.
	var pending, drifted []string
	for _, seg := range sr.PolicyTunNATSegments {
		if !needsApply(seg) {
			continue
		}
		if applied[seg] && len(covered[seg]) > 0 && masq[seg] {
			drifted = append(drifted, seg)
			continue
		}
		pending = append(pending, seg)
	}
	if len(pending) == 0 && len(drifted) == 0 {
		return
	}
	if len(pending) > 0 {
		s.appLog.Info("policy-tun-reconcile", iface,
			"применяем source-preserve к сегментам: "+strings.Join(pending, ", "))
	}
	if len(drifted) > 0 {
		s.appLog.Warn("policy-tun-reconcile", iface,
			"сегменты вернулись на динамический NAT мимо нас — применяем source-preserve повторно: "+strings.Join(drifted, ", "))
	}
	recorded, err := s.applyPolicyTunSourcePreserve(ctx, append(pending, drifted...), st.NATSegments)
	// Персист пишем ДО проверки ошибки: сегменты, до которых apply дошёл,
	// изменены на роутере, и без записи их не вернул бы ни teardown, ни revoke.
	if merged := mergePolicyTunNATRecords(st.NATSegments, recorded); len(merged) != len(st.NATSegments) {
		st.NATSegments = merged
		if perr := s.deps.Settings.SetPolicyTunState(st); perr != nil {
			s.appLog.Warn("policy-tun-reconcile", iface, "persist nat segments: "+perr.Error())
		}
	}
	if err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, "apply source-preserve: "+err.Error())
	}
}
