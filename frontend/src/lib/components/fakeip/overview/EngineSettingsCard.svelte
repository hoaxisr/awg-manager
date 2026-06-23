<!--
  «Настройки движка» по мокапу dash3 (`.eform` — 2-колоночный грид строк
  ключ/значение), но с РЕДАКТИРУЕМЫМИ контролами на РЕАЛЬНЫХ данных. Все поля
  round-trip через GET/PUT /singbox/router/settings; сохранение — общий
  sb-router паттерн mergeAndSaveSettings (patch → PUT → loadAll).

    - Движок: «Перезапустить» (onRestart → api.singboxControl) + тумблер ON при
      routingMode==='fakeip-tun' && enabled. Сам API НЕ дёргает — onToggleEngine
      запрашивает смену режима (диалог подтверждения рендерит страница).
    - TCP/IP-стек: Dropdown gvisor / system (settings.fakeipStack). system —
      ниже throughput-потолок, backend форсит gso:false.
    - WAN-интерфейс: «Авто» + список api.singboxRouterListWANInterfaces()
      (kernel-имя + label). Тот же discriminator, что sb-router StatusDrawer:
      Авто → {wanAutoDetect:true, wanInterface:''}; иначе {wanAutoDetect:false,
      wanInterface:name}.
    - Sniffing (SNI/host): Toggle settings.snifferEnabled.
    - fakeip-пул: Input'ы v4 (fakeipPool4) и v6 (fakeipPool6, пусто = v6 off).
      Лёгкая клиентская проверка CIDR; backend валидирует авторитетно и вернёт
      ошибку → notifications.
    - MTU tun: числовой Input (fakeipMtu, 576–9000).
    - Active iface: status.fakeipIface (e.g. «opkgtun0»), если провижен.
    - DNS для клиентов: status.fakeipDns (read-only) — адрес, который нужно
      прописать на клиентах вручную; авто-fallback нет.

  Сохранение применяется с перезапуском sing-box (бэкенд провижнит пул/MTU при
  enable). Значения и хендлеры приходят пропами; список WAN грузим сами.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { Toggle, Dropdown, Input, type DropdownOption } from '$lib/components/ui';
	import { RotateCw, TriangleAlert } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { mergeAndSaveSettings } from '$lib/components/sb-router/settingsActions';
	import type { SingboxRouterSettings, SingboxRouterWANInterface } from '$lib/types';

	interface Props {
		/** Движок включён в режиме fakeip-tun (routingMode==='fakeip-tun' && enabled). */
		engineOn: boolean;
		/** WAN: авто-детект интерфейса. */
		wanAutoDetect: boolean;
		/** WAN: явный системный интерфейс (когда не авто). */
		wanInterface?: string;
		/** Sniffing включён. */
		snifferEnabled: boolean;
		/** TCP/IP-стек fakeip-tun. */
		fakeipStack?: 'gvisor' | 'system';
		/** fakeip-пул v4 (CIDR). */
		fakeipPool4?: string;
		/** fakeip-пул v6 (CIDR; пусто = v6 выключен). */
		fakeipPool6?: string;
		/** MTU tun-интерфейса. */
		fakeipMtu?: number;
		/** Активный fakeip tun-интерфейс из статуса (e.g. «opkgtun0»); опционально. */
		fakeipIface?: string;
		/** DNS-адрес для ручной настройки клиентов (read-only из статуса). */
		fakeipDns?: string;
		/** Запрос смены движка — диалог подтверждения рендерит страница. */
		onToggleEngine: (turnOn: boolean) => void;
		/** Перезапуск sing-box — страница зовёт api.singboxControl('restart'). */
		onRestart: () => void | Promise<void>;
		/** Блокирует тумблер, пока переключение в полёте (управляется страницей). */
		toggleBusy?: boolean;
	}

	let {
		engineOn,
		wanAutoDetect,
		wanInterface,
		snifferEnabled,
		fakeipStack,
		fakeipPool4,
		fakeipPool6,
		fakeipMtu,
		fakeipIface,
		fakeipDns,
		onToggleEngine,
		onRestart,
		toggleBusy = false,
	}: Props = $props();

	let restarting = $state(false);
	let saving = $state(false);

	// WAN-интерфейсы для пикера (kernel-имя + label). Грузим лениво при монтаже.
	let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);
	onMount(async () => {
		try {
			wanInterfaces = await api.singboxRouterListWANInterfaces();
		} catch {
			wanInterfaces = [];
		}
	});

	// Драфты текстовых полей (CIDR/MTU) — правим локально, коммитим на change/blur.
	const pool4Draft = $state({ v: '' });
	const pool6Draft = $state({ v: '' });
	const mtuDraft = $state({ v: '' });
	// Синхронизируем драфты с пропами, когда сами не сохраняем.
	$effect(() => {
		if (!saving) pool4Draft.v = fakeipPool4 ?? '';
	});
	$effect(() => {
		if (!saving) pool6Draft.v = fakeipPool6 ?? '';
	});
	$effect(() => {
		if (!saving) mtuDraft.v = fakeipMtu != null ? String(fakeipMtu) : '';
	});

	const stackOptions: DropdownOption<'gvisor' | 'system'>[] = [
		{ value: 'gvisor', label: 'gvisor' },
		{ value: 'system', label: 'system' },
	];

	// WAN-пикер: «Авто» + kernel-интерфейсы. value '' = авто.
	const wanOptions = $derived<DropdownOption[]>([
		{ value: '', label: 'Авто' },
		...wanInterfaces.map((i) => ({
			value: i.name,
			label: i.label ? `${i.name} — ${i.label}` : i.name,
		})),
	]);
	const wanValue = $derived(wanAutoDetect ? '' : (wanInterface ?? ''));

	const stackHint = $derived(
		fakeipStack === 'system'
			? 'ниже throughput-потолок, требует gso:false'
			: undefined,
	);

	// Лёгкая клиентская проверка CIDR (backend валидирует авторитетно).
	const CIDR4 = /^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/;
	const CIDR6 = /^[0-9a-fA-F:]+\/\d{1,3}$/;

	async function save(patch: Partial<SingboxRouterSettings>): Promise<void> {
		if (saving) return;
		saving = true;
		try {
			await mergeAndSaveSettings(patch);
		} catch (e) {
			const msg = e instanceof Error ? e.message : 'Не удалось сохранить настройки';
			notifications.error(msg);
		} finally {
			saving = false;
		}
	}

	function handleStack(v: 'gvisor' | 'system'): void {
		if (v === (fakeipStack ?? 'gvisor')) return;
		void save({ fakeipStack: v });
	}

	function handleWan(v: string): void {
		if (v === '') void save({ wanAutoDetect: true, wanInterface: '' });
		else void save({ wanAutoDetect: false, wanInterface: v });
	}

	function handleSniffer(next: boolean): void {
		void save({ snifferEnabled: next });
	}

	function commitPool4(): void {
		const v = pool4Draft.v.trim();
		if (v === (fakeipPool4 ?? '')) return;
		if (v !== '' && !CIDR4.test(v)) {
			notifications.error('Некорректный IPv4-CIDR пула (пример: 198.18.0.0/15)');
			pool4Draft.v = fakeipPool4 ?? '';
			return;
		}
		void save({ fakeipPool4: v });
	}

	function commitPool6(): void {
		const v = pool6Draft.v.trim();
		if (v === (fakeipPool6 ?? '')) return;
		// Пусто — допустимо (v6 выключен). Иначе лёгкая проверка CIDR6.
		if (v !== '' && !CIDR6.test(v)) {
			notifications.error('Некорректный IPv6-CIDR пула (пусто = выкл; пример: fc00::/18)');
			pool6Draft.v = fakeipPool6 ?? '';
			return;
		}
		void save({ fakeipPool6: v });
	}

	function commitMtu(): void {
		const raw = mtuDraft.v.trim();
		const n = Number(raw);
		if (raw === '' || !Number.isInteger(n) || n < 576 || n > 9000) {
			notifications.error('MTU должен быть целым числом в диапазоне 576–9000');
			mtuDraft.v = fakeipMtu != null ? String(fakeipMtu) : '';
			return;
		}
		if (n === fakeipMtu) return;
		void save({ fakeipMtu: n });
	}

	async function handleRestart(): Promise<void> {
		if (restarting || !engineOn) return;
		restarting = true;
		try {
			await onRestart();
		} finally {
			restarting = false;
		}
	}
</script>

<div class="panel">
	<div class="ph">
		<span class="nm">Настройки движка</span>
		<span class="meta">применяются с перезапуском sing-box</span>
	</div>

	<div class="eform">
		<!-- Движок: перезапуск + тумблер (controlled — страница решает смену). -->
		<div class="erow">
			<span class="k">Движок</span>
			<span class="val">
				<button
					class="restart"
					type="button"
					onclick={handleRestart}
					disabled={restarting || !engineOn}
				>
					{#if restarting}
						<RotateCw size={14} class="spin" />
					{/if}
					Перезапустить
				</button>
				<Toggle
					checked={engineOn}
					controlled
					loading={toggleBusy}
					size="sm"
					onchange={(next) => onToggleEngine(next)}
				/>
			</span>
		</div>

		<!-- TCP/IP-стек: gvisor / system. -->
		<div class="erow">
			<span class="k">TCP/IP-стек</span>
			<span class="val ctl">
				<Dropdown
					value={fakeipStack ?? 'gvisor'}
					options={stackOptions}
					disabled={saving}
					fullWidth
					onchange={handleStack}
				/>
				{#if stackHint}
					<span class="ctl-hint">{stackHint}</span>
				{/if}
			</span>
		</div>

		<!-- WAN-интерфейс: Авто + kernel-список. -->
		<div class="erow">
			<span class="k">WAN-интерфейс</span>
			<span class="val ctl">
				<Dropdown
					value={wanValue}
					options={wanOptions}
					disabled={saving}
					fullWidth
					onchange={handleWan}
				/>
			</span>
		</div>

		<!-- Sniffing (SNI/host). -->
		<div class="erow">
			<span class="k">Sniffing (SNI/host)</span>
			<span class="val">
				<Toggle
					checked={snifferEnabled}
					size="sm"
					loading={saving}
					onchange={handleSniffer}
				/>
			</span>
		</div>

		<!-- DNS для клиентов (read-only из статуса). -->
		{#if fakeipDns}
			<div class="erow">
				<span class="k">DNS для клиентов</span>
				<span class="val ctl">
					<code class="iface">{fakeipDns}</code>
					<span class="warn">
						<TriangleAlert size={13} />
						Пропишите этот DNS на клиентах вручную. Авто-fallback при падении прокси нет.
					</span>
				</span>
			</div>
		{/if}

		<!-- Подсказка: policy-exit через opkgtun. -->
		<div class="erow erow-hint">
			<span class="policy-hint">
				Чтобы завернуть весь трафик устройства через fakeip — назначьте его в
				<a href="/routing?tab=policy" class="policy-link">Политике доступа</a>
				на выход opkgtun.
			</span>
		</div>

		<!-- fakeip-пул v4. -->
		<div class="erow">
			<span class="k">fakeip-пул v4</span>
			<span class="val ctl">
				<Input
					value={pool4Draft.v}
					placeholder="198.18.0.0/15"
					disabled={saving}
					fullWidth
					oninput={(v) => (pool4Draft.v = v)}
					onchange={commitPool4}
				/>
			</span>
		</div>

		<!-- fakeip-пул v6 (пусто = выключен). -->
		<div class="erow">
			<span class="k">fakeip-пул v6</span>
			<span class="val ctl">
				<Input
					value={pool6Draft.v}
					placeholder="fc00::/18 (пусто — выкл)"
					disabled={saving}
					fullWidth
					oninput={(v) => (pool6Draft.v = v)}
					onchange={commitPool6}
				/>
			</span>
		</div>

		<!-- MTU tun. -->
		<div class="erow">
			<span class="k">MTU tun</span>
			<span class="val ctl">
				<Input
					type="number"
					value={mtuDraft.v}
					placeholder="1500"
					disabled={saving}
					fullWidth
					oninput={(v) => (mtuDraft.v = v)}
					onchange={commitMtu}
				/>
			</span>
		</div>

		<!-- Активный tun-интерфейс (только когда провижен). -->
		{#if fakeipIface}
			<div class="erow">
				<span class="k">tun-интерфейс</span>
				<span class="val"><span class="iface">{fakeipIface}</span></span>
			</div>
		{/if}
	</div>
</div>

<style>
	.panel {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 12px);
		padding: 1rem;
	}

	.ph {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.625rem;
	}

	.ph .nm {
		color: var(--text-primary);
		font-size: 1.0625rem;
		font-weight: 700;
	}

	.ph .meta {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	/* 2-колоночный грид строк с тонкими разделителями (dash3 `.eform`). */
	.eform {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1px;
		background: var(--color-border);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 8px);
		overflow: hidden;
	}

	.erow {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.75rem 0.875rem;
		background: var(--color-bg-secondary);
		font-size: 0.875rem;
	}

	.erow .k {
		color: var(--text-secondary);
		flex-shrink: 0;
	}

	.erow .val {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	/* Контрол-ячейка (Dropdown/Input) тянется на доступную ширину. */
	.erow .val.ctl {
		flex: 1;
		min-width: 0;
		flex-direction: column;
		align-items: stretch;
		gap: 0.25rem;
	}

	.ctl-hint {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.warn {
		display: flex;
		align-items: flex-start;
		gap: 0.3125rem;
		color: var(--text-muted);
		font-size: 0.8125rem;
		line-height: 1.4;
	}

	.warn :global(svg) {
		flex-shrink: 0;
		margin-top: 0.125rem;
	}

	.erow-hint {
		grid-column: 1 / -1;
		align-items: flex-start;
	}

	.policy-hint {
		color: var(--text-muted);
		font-size: 0.8125rem;
		line-height: 1.4;
	}

	.policy-link {
		color: var(--color-accent);
		text-decoration: none;
	}

	.policy-link:hover {
		text-decoration: underline;
	}

	.iface {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		padding: 0.3125rem 0.625rem;
		color: var(--color-accent);
		font-size: 0.875rem;
		font-family: var(--font-mono, monospace);
	}

	.restart {
		color: var(--text-primary);
		background: none;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		padding: 0.3125rem 0.6875rem;
		font-size: 0.8125rem;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
	}

	.restart:hover:not(:disabled) {
		border-color: var(--color-border-hover);
	}

	.restart:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.restart :global(.spin) {
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	@media (max-width: 760px) {
		.eform {
			grid-template-columns: 1fr;
		}
	}
</style>
