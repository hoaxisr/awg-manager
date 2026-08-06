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

/**
 * Что происходит с netfilter-исключениями в текущем режиме.
 *
 * @param mode           текущий режим захвата (нормализованный).
 * @param qosClassCount  сколько ВКЛЮЧЁННЫХ классов QoS настроено. Значим только
 *                       для policy-tun: без классов бэкенд не ставит там
 *                       netfilter вообще.
 */
export function netfilterExclusionsScope(
	mode: EngineRoutingMode,
	qosClassCount: number,
): ExclusionsScope {
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
		return qosClassCount > 0
			? {
					applies: true,
					notice:
						'В режиме «Политики + tun» эти исключения действуют только внутри цепочки ' +
						'QoS-классов — то есть на трафике, помеченном DSCP. Остальной трафик ' +
						'заворачивает политика доступа NDMS, и порты с подсетями его не касаются.',
				}
			: {
					applies: false,
					notice:
						'В режиме «Политики + tun» netfilter-правила ставятся только вместе с классами ' +
						'QoS. Ни одного включённого класса нет — эти порты и подсети сейчас ни на что ' +
						'не влияют.',
				};
	}
	return { applies: true, notice: null };
}
