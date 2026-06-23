<!--
  «Устройства»-чип FakeIP по мокапу page-devices: ОДНА карточка на всю ширину —
  таблица NDMS-устройств (hotspot) + персональная привязка (route-правило по
  source_ip_cidr → outbound).

  Источники (переиспользование, не пересборка):
    - устройства: общий polling-store routing.policyDevicesStore
      (api.listPolicyDevices, NDMS hotspot). Подписка сама стартует опрос (30с).
    - назначение (персональный/прямой): чистый resolveDeviceTargeting
      (deviceTargeting.ts), на вход — route-правила (fakeipConfig.rules).
      ipInCIDR из utils/cidr.
    - имя привязанного outbound: лейбл из fakeipConfig.options (тот же каталог,
      что route-final/RuleEditModal).
    - live-соединения per-IP: liveConnectionsSnapshot (sb-router) — счётчик по
      metadata.sourceIP. Это ЖИВОЙ сигнал → показываем число только при движке
      live; иначе «—» (честность §12.1). Store биндится в FakeIPPageShell.
    - привязка: фокус-пикер (Dropdown outbound'ов) → add/update route-правила
      {source_ip_cidr:[<ip>/32], outbound}. Снятие — delete по индексу + Confirm.

  Движок-гейт: список устройств — информационный (NDMS), рендерится при любом
  состоянии движка; счётчик соединений деградирует по engineState.live.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { fakeipConfig } from '$lib/stores/fakeipConfig';
	import { policyDevicesStore } from '$lib/stores/routing';
	import { liveConnectionsSnapshot } from '$lib/components/sb-router/liveConnectionsStore';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, ConfirmModal, Dropdown } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import { Pencil, X } from 'lucide-svelte';
	import type { PolicyDevice, SingboxRouterRule } from '$lib/types';
	import {
		resolveDeviceTargeting,
		type DeviceTargeting,
	} from './deviceTargeting';
	import type { FakeIPEngineState } from '../engineState';

	let { engineState }: { engineState: FakeIPEngineState } = $props();

	// Живой сигнал доступен только при работающем движке + достижимом Clash.
	const live = $derived(engineState === 'live');

	// ── Источники ──────────────────────────────────────────────────────────
	const devicesState = policyDevicesStore; // подписка стартует опрос (30с)
	const storeRules = fakeipConfig.rules;
	const storeOptions = fakeipConfig.options;

	onMount(() => {
		// rules/options живут в fakeipConfig; прямой заход на чип может застать
		// store холодным — идемпотентно.
		void fakeipConfig.loadAll();
	});

	const devices = $derived<PolicyDevice[]>($devicesState.data ?? []);

	// outbound tag → отображаемый лейбл (для бейджа персональной привязки).
	const outboundLabels = $derived.by(() => {
		const m = new Map<string, string>();
		for (const g of $storeOptions) {
			for (const it of g.items) m.set(it.value, it.label);
		}
		return m;
	});

	function outboundLabel(tag: string | null): string {
		if (!tag) return '';
		if (tag === 'direct') return 'direct';
		return outboundLabels.get(tag) ?? tag;
	}

	// Live-счётчик соединений per source IP. Пересчёт на каждый снапшот WS.
	const connCountByIP = $derived.by(() => {
		const m = new Map<string, number>();
		for (const c of $liveConnectionsSnapshot.connections) {
			const ip = c.metadata?.sourceIP;
			if (!ip) continue;
			m.set(ip, (m.get(ip) ?? 0) + 1);
		}
		return m;
	});

	interface DeviceRow {
		device: PolicyDevice;
		targeting: DeviceTargeting;
		conns: number;
	}

	const rows = $derived<DeviceRow[]>(
		devices.map((device) => ({
			device,
			targeting: resolveDeviceTargeting(device.ip, $storeRules),
			conns: connCountByIP.get(device.ip) ?? 0,
		})),
	);

	function isOnline(device: PolicyDevice): boolean {
		return device.active;
	}

	function modeLabel(mode: DeviceTargeting['mode']): string {
		return mode === 'personal' ? 'персональный' : 'прямой';
	}

	// ── Привязка: фокус-пикер outbound'а ───────────────────────────────────
	// Опции = direct + все outbounds, кроме «Специальные» (тот же набор, что
	// route-final в RoutesTab / RuleEditModal).
	const bindOptions = $derived<DropdownOption[]>([
		{ value: 'direct', label: 'direct (мимо VPN)' },
		...$storeOptions
			.filter((g) => g.group !== 'Специальные')
			.flatMap((g) => g.items.map((it) => ({ value: it.value, label: it.label, group: g.group }))),
	]);

	let bindOpen = $state(false);
	let bindDevice = $state<PolicyDevice | null>(null);
	let bindRuleIndex = $state<number | null>(null);
	let bindDraft = $state('direct');
	let bindBusy = $state(false);

	function openBind(row: DeviceRow): void {
		bindDevice = row.device;
		bindRuleIndex = row.targeting.ruleIndex;
		bindDraft = row.targeting.outbound ?? 'direct';
		bindOpen = true;
	}

	function closeBind(): void {
		if (bindBusy) return;
		bindOpen = false;
		bindDevice = null;
		bindRuleIndex = null;
	}

	async function saveBind(): Promise<void> {
		if (bindBusy || !bindDevice) return;
		bindBusy = true;
		const ip = bindDevice.ip;
		const rule: SingboxRouterRule = {
			source_ip_cidr: [`${ip}/32`],
			outbound: bindDraft,
		};
		try {
			if (bindRuleIndex !== null) {
				await api.singboxFakeIPUpdateRule(bindRuleIndex, rule);
			} else {
				await api.singboxFakeIPAddRule(rule);
			}
			await fakeipConfig.loadAll();
			notifications.success(`Привязка устройства ${bindDevice.name || ip} сохранена`);
			bindOpen = false;
			bindDevice = null;
			bindRuleIndex = null;
		} catch (e) {
			notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			bindBusy = false;
		}
	}

	// ── Снятие привязки (только у персональных) ─────────────────────────────
	let unbindRow = $state<DeviceRow | null>(null);
	let unbindBusy = $state(false);

	async function confirmUnbind(): Promise<void> {
		if (!unbindRow || unbindRow.targeting.ruleIndex === null) return;
		unbindBusy = true;
		try {
			await api.singboxFakeIPDeleteRule(unbindRow.targeting.ruleIndex);
			await fakeipConfig.loadAll();
			notifications.success('Персональная привязка снята');
			unbindRow = null;
		} catch (e) {
			notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			unbindBusy = false;
		}
	}
</script>

<section class="panel">
	<header class="ph">
		<span class="nm">Устройства · {devices.length}</span>
		<span class="src">из NDMS hotspot</span>
	</header>
	<p class="pd">
		<b class="m-pers">персональный</b> — привязка к конкретному outbound (route-правило по
		source_ip_cidr). <b class="m-dir">прямой</b> — без персональной привязки, по общим правилам.
		Pencil — задать персональную привязку, крестик — снять (активен только у персональных).
	</p>

	<div class="table">
		<div class="thead">
			<span class="th-dev">устройство</span>
			<span class="th-addr">адрес</span>
			<span class="th-st">статус</span>
			<span class="th-mode">назначение</span>
			<span class="th-conns">соединения</span>
			<span class="th-acts" aria-hidden="true"></span>
		</div>

		<div class="rows">
			{#if rows.length === 0}
				<div class="empty">Устройства NDMS hotspot не найдены.</div>
			{/if}

			{#each rows as row (row.device.mac)}
				{@const online = isOnline(row.device)}
				{@const personal = row.targeting.mode === 'personal'}
				<div class="drow">
					<div class="cell-dev">
						<span class="dev">{row.device.name || row.device.hostname || row.device.mac}</span>
						<span class="mac">{row.device.mac}</span>
					</div>
					<span class="ip">{row.device.ip}</span>
					<span class="st" class:off={!online}>
						<span class="dot" aria-hidden="true"></span>
						{online ? 'онлайн' : 'офлайн'}
					</span>
					<span class="mode-cell">
						<span
							class="mode"
							class:pers={personal}
						>{modeLabel(row.targeting.mode)}</span>
						{#if personal}
							<span class="ob" title={outboundLabel(row.targeting.outbound)}>
								{outboundLabel(row.targeting.outbound)}
							</span>
						{/if}
					</span>
					<span class="conns" class:zero={live && row.conns === 0}>
						{#if live}
							<span class="cdot" aria-hidden="true"></span>{row.conns}
						{:else}
							—
						{/if}
					</span>
					<div class="acts">
						<button
							type="button"
							class="ib"
							onclick={() => openBind(row)}
							aria-label={`Задать привязку для ${row.device.name || row.device.ip}`}
							title="Задать персональную привязку"
						>
							<Pencil size={15} strokeWidth={2} />
						</button>
						<button
							type="button"
							class="ib"
							disabled={!personal}
							onclick={() => (personal ? (unbindRow = row) : undefined)}
							aria-label={`Снять привязку для ${row.device.name || row.device.ip}`}
							title={personal ? 'Снять персональную привязку' : 'Привязки нет'}
						>
							<X size={15} strokeWidth={2} />
						</button>
					</div>
				</div>
			{/each}
		</div>
	</div>
</section>

<!-- ── Фокус-пикер привязки устройства к outbound ─────────────────────── -->
<Modal
	open={bindOpen}
	title={bindDevice ? `Привязка · ${bindDevice.name || bindDevice.ip}` : 'Привязка устройства'}
	size="sm"
	onclose={closeBind}
>
	{#if bindDevice}
		<p class="bind-info">
			Привязать <b>{bindDevice.name || bindDevice.hostname || bindDevice.mac}</b>
			(<span class="bind-ip">{bindDevice.ip}</span>) к outbound. Создаёт route-правило по
			source_ip_cidr — трафик устройства уйдёт в выбранный outbound, минуя общие правила.
		</p>
		<Dropdown bind:value={bindDraft} options={bindOptions} label="Outbound" fullWidth />
	{/if}
	{#snippet actions()}
		<button type="button" class="btn ghost" onclick={closeBind} disabled={bindBusy}>Отмена</button>
		<button type="button" class="btn primary" onclick={saveBind} disabled={bindBusy}>
			{bindBusy ? 'Сохранение…' : 'Сохранить'}
		</button>
	{/snippet}
</Modal>

<ConfirmModal
	open={unbindRow !== null}
	title="Снять привязку"
	message={unbindRow
		? `Снять персональную привязку устройства ${unbindRow.device.name || unbindRow.device.ip}? Оно вернётся к общим правилам.`
		: ''}
	busy={unbindBusy}
	onConfirm={confirmUnbind}
	onClose={() => {
		if (!unbindBusy) unbindRow = null;
	}}
/>

<style>
	.panel {
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px solid var(--color-border, var(--border));
		border-radius: var(--radius, 12px);
		padding: 1rem;
		min-width: 0;
	}

	.ph {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.25rem;
	}
	.nm {
		color: var(--text-primary);
		font-size: 0.875rem;
		font-weight: 700;
	}
	.src {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.pd {
		color: var(--text-muted);
		font-size: 0.8125rem;
		line-height: 1.5;
		margin: 0 0 0.875rem;
	}
	.pd .m-pers {
		color: var(--color-info, #8aa0e0);
	}
	.pd .m-dir {
		color: var(--text-secondary);
	}

	.empty {
		padding: 0.875rem;
		color: var(--text-muted);
		text-align: center;
		font-size: 0.8125rem;
	}

	.table {
		min-width: 0;
	}
	/* Грид-раскладка общая для шапки и строк. */
	.thead,
	.drow {
		display: grid;
		grid-template-columns:
			minmax(0, 1.4fr) minmax(7rem, 0.8fr) minmax(5.5rem, auto)
			minmax(0, 1.1fr) minmax(5rem, auto) auto;
		align-items: center;
		gap: 0.6rem;
	}
	.thead {
		padding: 0.45rem 0.25rem;
		border-bottom: 1px solid var(--color-border, var(--border));
	}
	.thead span {
		color: var(--text-muted);
		font-size: 0.6875rem;
		font-weight: 500;
	}
	.th-conns {
		text-align: right;
	}

	.rows {
		display: flex;
		flex-direction: column;
	}
	.drow {
		padding: 0.6rem 0.25rem;
		border-bottom: 1px solid var(--color-border-subtle, var(--border));
	}
	.drow:last-child {
		border-bottom: none;
	}

	.cell-dev {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		min-width: 0;
	}
	.dev {
		color: var(--text-primary);
		font-weight: 600;
		font-size: 0.8125rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.mac {
		color: var(--text-muted);
		font-size: 0.6875rem;
		font-family: var(--font-mono);
	}

	.ip {
		color: var(--text-secondary);
		font-size: 0.8125rem;
		font-family: var(--font-mono);
	}

	.st {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.75rem;
		color: var(--text-secondary);
	}
	.st .dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-success, #7bd88f);
	}
	.st.off {
		color: var(--text-muted);
	}
	.st.off .dot {
		background: var(--color-border, #3a3a3c);
	}

	.mode-cell {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		min-width: 0;
	}
	.mode {
		font-size: 0.6875rem;
		border-radius: 5px;
		padding: 2px 8px;
		border: 1px solid var(--color-border, var(--border));
		color: var(--text-muted);
		white-space: nowrap;
	}
	.mode.pers {
		color: var(--color-info, #8aa0e0);
		border-color: color-mix(in srgb, var(--color-info, #8aa0e0) 40%, transparent);
	}
	.ob {
		color: var(--color-accent, var(--accent));
		font-weight: 600;
		font-size: 0.75rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}

	.conns {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.35rem;
		font-size: 0.75rem;
		font-family: var(--font-mono);
		color: var(--color-success, #7bd88f);
	}
	.conns.zero {
		color: var(--text-muted);
	}
	.conns .cdot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: currentColor;
	}

	.acts {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.25rem;
		flex-shrink: 0;
	}
	.ib {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--text-muted);
		background: transparent;
		border: 1px solid var(--color-border, var(--border));
		border-radius: var(--radius-sm, 6px);
		padding: 0.25rem;
		cursor: pointer;
	}
	.ib:hover:not(:disabled) {
		color: var(--text-primary);
		border-color: var(--color-border-hover, var(--border));
	}
	.ib:disabled {
		opacity: 0.35;
		cursor: not-allowed;
	}

	/* ── Модал привязки ── */
	.bind-info {
		margin: 0 0 0.875rem;
		font-size: 0.8125rem;
		line-height: 1.5;
		color: var(--text-secondary);
	}
	.bind-ip {
		font-family: var(--font-mono);
		color: var(--text-primary);
	}
	.btn {
		font-size: 0.8125rem;
		font-weight: 600;
		border-radius: var(--radius-sm, 6px);
		padding: 0.4rem 0.85rem;
		cursor: pointer;
		border: 1px solid var(--color-border, var(--border));
		background: transparent;
		color: var(--text-primary);
	}
	.btn:disabled {
		opacity: 0.5;
		cursor: default;
	}
	.btn.primary {
		background: var(--color-accent, var(--accent));
		border-color: var(--color-accent, var(--accent));
		color: var(--color-on-accent, #0a0a0a);
	}
	.btn.ghost:hover:not(:disabled) {
		border-color: var(--color-border-hover, var(--border));
	}
</style>
