// Pure model for the FakeIP "Обзор" composite readiness panel (FE-spec §12.2).
// Side-effect-free so the row derivation can be unit-tested without mounting
// the Svelte component.
//
// HONESTY (§2): only THREE of the five FE-spec indicators are exposed by the
// backend today (sing-box process, pool auto-route, source-preservation). The
// remaining two (DNS `.2` delivery, egress health) are NOT in the Status DTO,
// so they are rendered as «не сообщается движком» — never fabricated green.

/** Visual state of a readiness dot. `unknown` = honest "не определено/не сообщается". */
export type ReadinessState = 'ok' | 'warn' | 'error' | 'unknown';

export interface ReadinessRow {
	/** Stable key for keyed #each. */
	key: string;
	/** Short Russian label. */
	label: string;
	state: ReadinessState;
	/** Short Russian state text shown next to the dot. */
	text: string;
}

export interface ReadinessInput {
	/** sing-box daemon process is running ($singboxStatus.data?.running). LIVE. */
	running: boolean;
	/** NDMS pool auto-route present (SingboxRouterStatus.active). LIVE. */
	active: boolean;
	/**
	 * fakeip-tun source-preservation verdict (SingboxRouterStatus.sourcePreserved).
	 * Tri-state: true → ok, false → SNAT warning, undefined → не определено. LIVE.
	 */
	sourcePreserved?: boolean;
}

/**
 * Build the ordered readiness-row models for the Обзор panel. Three rows are
 * driven by live backend signals; the last two are always «не сообщается» —
 * the backend Status DTO does not expose them yet (deferred to Slice 3+/2.2).
 */
export function readinessRows(input: ReadinessInput): ReadinessRow[] {
	const rows: ReadinessRow[] = [];

	// 1. sing-box process — LIVE.
	rows.push({
		key: 'singbox',
		label: 'Sing-box',
		state: input.running ? 'ok' : 'error',
		text: input.running ? 'процесс запущен' : 'процесс остановлен',
	});

	// 2. Pool auto-route present — LIVE. For fakeip-tun, active == pool route present.
	rows.push({
		key: 'route',
		label: 'Маршрут пула',
		state: input.active ? 'ok' : 'error',
		text: input.active ? 'auto-маршрут активен' : 'маршрут отсутствует',
	});

	// 3. Source-preservation — LIVE tri-state.
	rows.push({
		key: 'source',
		label: 'Сохранение source',
		state:
			input.sourcePreserved === undefined
				? 'unknown'
				: input.sourcePreserved
					? 'ok'
					: 'warn',
		text:
			input.sourcePreserved === undefined
				? 'не определено'
				: input.sourcePreserved
					? 'source сохраняется'
					: 'возможен SNAT (source не сохранён)',
	});

	// 4. DNS `.2` delivery — NOT reported by the Status DTO (deferred).
	rows.push({
		key: 'dns',
		label: 'Доставка DNS',
		state: 'unknown',
		text: 'не сообщается движком',
	});

	// 5. Egress health — NOT reported by the Status DTO (deferred).
	rows.push({
		key: 'egress',
		label: 'Egress',
		state: 'unknown',
		text: 'не сообщается движком',
	});

	return rows;
}
