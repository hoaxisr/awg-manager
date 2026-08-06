// Где исключения на самом деле работают, а где карточка врала.
//
// В шторке (StatusDrawer:584-606) карточка «Исключения» была одна и без единого
// режимного гейта — только `isExpert`. На единой странице «Движка» это враньё
// стало видно: карточка одинаково предлагает порты, подсети и пресеты во всех
// трёх режимах, а бэкенд ставит netfilter-исключения далеко не везде.
//
// СВЕРЕНО ПО КОДУ БЭКЕНДА:
//
//   tproxy      — service_lifecycle.go:711-712, 1664-1665, 1739-1740:
//                 resolveBypassPorts/resolveBypassCIDRs уходят в
//                 RestoreInputSpec на всех путях (enable и reconcile).
//                 Исключения работают полностью.
//   policy-tun  — policytun_enable.go:352-370 и policytun_reconcile.go:331-341:
//                 весь блок с bypass-портами и CIDR стоит ВНУТРИ `if
//                 len(qosSpecs) > 0`, а спека — с `DSCPOnly: true`. То есть
//                 исключения попадают только в DSCP-цепочку и только когда
//                 настроен хотя бы один класс QoS. Без классов netfilter в этом
//                 режиме не трогают вовсе — трафик заворачивает политика
//                 доступа NDMS.
//                 ВАЖНО: `qosSpecs` обнуляется ЕЩЁ ДВАЖДЫ до этой проверки —
//                 при недоступном netfilter (`:337-341`) и при непригодном
//                 xt_dscp (`:343-350`). Считать одни только включённые классы
//                 значит врать ровно так же, как врал режимный гейт: «один
//                 класс есть» при незагруженном xt_dscp даёт спокойную подпись
//                 «действуют внутри цепочки QoS», хотя бэкенд не поставил
//                 ничего и написал в журнал «классы QoS пропущены».
//   fakeip-tun  — ни одного вызова resolveBypassPorts/resolveBypassCIDRs:
//                 перехвата на netfilter нет, весь трафик заходит в sing-box
//                 через tun. Порты и подсети не применяются вообще.
//
// ПОЧЕМУ КАРТОЧКУ НЕ ПРЯЧЕМ ЦЕЛИКОМ. Пресет `keendns` — не порты и не CIDR, а
// DNS-перезапись (presets.go:46-56 «Портов/CIDR нет», keendns_sync.go): она
// работает во всех трёх режимах. Спрятать карточку в FakeIP значило бы унести
// вместе с враньём работающую настройку.
import type { EngineRoutingMode } from './engineRunState';

/** Id пресетов, которые работают НЕ через netfilter. Сегодня это только
 *  keendns: свой KeenDNS/CrazeDNS FQDN → LAN IP через DNS-перезапись. */
export const DNS_BYPASS_PRESET_IDS: readonly string[] = ['keendns'];

export function isDnsBypassPreset(id: string): boolean {
	return DNS_BYPASS_PRESET_IDS.includes(id);
}

/** Разводит пресеты по механизму: DNS-перезапись против netfilter. */
export function partitionBypassPresets<T extends { id: string }>(
	presets: readonly T[],
): { dns: T[]; netfilter: T[] } {
	return {
		dns: presets.filter((p) => isDnsBypassPreset(p.id)),
		netfilter: presets.filter((p) => !isDnsBypassPreset(p.id)),
	};
}

export interface ExclusionsScope {
	/** Применяются ли netfilter-исключения (порт-пресеты, доп. порты, подсети). */
	applies: boolean;
	/**
	 * Честная подпись под блоком. null — только там, где исключения работают
	 * без оговорок и объяснять нечего.
	 */
	notice: string | null;
}

export interface ExclusionsScopeInput {
	/** Текущий режим захвата (нормализованный). */
	mode: EngineRoutingMode;
	/**
	 * Сколько ВКЛЮЧЁННЫХ классов QoS настроено. Выключенный класс не попадает в
	 * `qosSpecs` бэкенда, значит цепочку из-за него не поставят.
	 */
	qosClassCount: number;
	/**
	 * `status.xtDscpAvailable`. Строго `false` — модуль точно непригоден;
	 * `undefined` — бэкенд не сказал (легаси-ответ), и выдумывать поломку нельзя.
	 * Тот же тристейт, что у соседа `QosSettingsCard` (`status.xtDscpAvailable
	 * === false`).
	 */
	xtDscpAvailable?: boolean | null;
	/** `status.netfilterAvailable`. Тот же тристейт. */
	netfilterAvailable?: boolean | null;
}

/** Что происходит с netfilter-исключениями при текущем режиме и состоянии ядра. */
export function netfilterExclusionsScope(input: ExclusionsScopeInput): ExclusionsScope {
	const { mode, qosClassCount, xtDscpAvailable, netfilterAvailable } = input;

	if (mode === 'fakeip-tun') {
		return {
			applies: false,
			notice:
				'В режиме FakeIP эти исключения не применяются: перехвата на netfilter нет, ' +
				'весь трафик заходит в sing-box через tun-интерфейс. Настройка сохранится и ' +
				'заработает после возврата на TPROXY.',
		};
	}

	if (mode === 'policy-tun') {
		// Порядок веток повторяет порядок обнулений qosSpecs в
		// policytun_enable.go: сначала netfilter, потом xt_dscp, потом сами классы.
		// Называть надо ПЕРВУЮ причину — чинить пользователю именно её.
		if (netfilterAvailable === false) {
			return {
				applies: false,
				notice:
					'В режиме «Политики + tun» netfilter-правила ставятся только вместе с классами ' +
					'QoS, а классы сейчас пропущены: netfilter на роутере недоступен. Эти порты и ' +
					'подсети ни на что не влияют.',
			};
		}
		if (xtDscpAvailable === false) {
			return {
				applies: false,
				notice:
					'В режиме «Политики + tun» netfilter-правила ставятся только вместе с классами ' +
					'QoS, а классы сейчас пропущены: модуль ядра xt_dscp недоступен. Эти порты и ' +
					'подсети ни на что не влияют.',
			};
		}
		if (qosClassCount === 0) {
			return {
				applies: false,
				notice:
					'В режиме «Политики + tun» netfilter-правила ставятся только вместе с классами ' +
					'QoS. Ни одного включённого класса нет — эти порты и подсети сейчас ни на что ' +
					'не влияют.',
			};
		}
		return {
			applies: true,
			notice:
				'В режиме «Политики + tun» эти исключения действуют только внутри цепочки ' +
				'QoS-классов — то есть на трафике, помеченном DSCP. Остальной трафик ' +
				'заворачивает политика доступа NDMS, и порты с подсетями его не касаются.',
		};
	}

	// TPROXY: bypass уходит в RestoreInputSpec на всех путях, независимо от
	// классов QoS и xt_dscp. Недоступный netfilter здесь не оговорка к
	// исключениям, а смерть всего перехвата — об этом говорят пилюля состояния и
	// карточка «Здоровье»; вторая копия той же тревоги тут была бы шумом.
	return { applies: true, notice: null };
}
