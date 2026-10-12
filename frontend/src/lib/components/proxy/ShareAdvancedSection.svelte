<script lang="ts">
	// SH-62..SH-71, SH-84/SH-85 — «Дополнительно» раздачи: экспертные поля
	// WDTT-сервера, бэкенд и режим туннеля FreeTurn-сервера, режим записи
	// server.log и освобождение портов. Свёрнута: глобального режима «Эксперт»
	// больше нет (решение Q7 ИА).
	import { m } from '$lib/i18n';
	import { Dropdown, FieldHint, Input, SegmentedControl } from '$lib/components/ui';
	import { modeOptions } from '../freeturn/options';
	import { listenPortNumber } from '$lib/utils/listenPortUtils';
	import { effectiveStaticWan } from '$lib/api/proxyInstances';
	import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';
	import KillPortSection from './KillPortSection.svelte';
	import { directListenValue, type SharePort } from './shareConfig';

	type StatsLogMode = 'ram' | 'off' | 'disk';

	interface Props {
		/** Редактируемая копия конфига WDTT-сервера. */
		wdttServer?: WdttServerConfig;
		/** Редактируемая копия конфига FreeTurn-сервера. */
		ftServer?: FreeTurnServerConfig;
		/** Порты инстанса — строка на каждый. */
		ports: SharePort[];
		/**
		 * WAN-интерфейсы роутера для режима NAT «Интернет». Пусто — список ещё
		 * не приехал либо не отдался: поле остаётся, но выбирать не из чего.
		 */
		wanOptions?: { value: string; label: string }[];
	}

	let {
		wdttServer = $bindable(),
		ftServer = $bindable(),
		ports,
		wanOptions = [],
	}: Props = $props();

	// Внутренний WG-порт: наружу на него не приходят, снаружи не виден.
	const wgPort = $derived(String(wdttServer?.wgPort || 56001));

	function applyWgPort(v: string) {
		if (!wdttServer) return;
		wdttServer.wgPort = Math.max(1, Math.min(65535, Number(v) || 56001));
	}

	// Direct-порт — НОМЕР, а не адрес: хост наследуется от порта раздачи
	// (`directListenValue`), иначе гарды фронта и бэкенда расходятся. Пусто —
	// выключено.
	const directPort = $derived(
		wdttServer?.directListen?.trim()
			? String(listenPortNumber(wdttServer.directListen, 0) || '')
			: '',
	);

	function applyDirectPort(v: string) {
		if (!wdttServer) return;
		const next = directListenValue(wdttServer.listen, v);
		if (next !== null) wdttServer.directListen = next;
	}

	const statsLogOptions: { value: StatsLogMode; label: string }[] = $derived([
		{ value: 'ram', label: 'RAM' },
		{ value: 'off', label: m.proxy_adv_off() },
		{ value: 'disk', label: 'Flash' },
	]);

	const statsLog = $derived((wdttServer?.statsLog?.trim() || 'ram') as StatsLogMode);
</script>

<DetailSection title={m.proxy_adv_title()} collapsed hint={m.proxy_adv_share_hint()}>
	{#if wdttServer}
		<div class="grid">
			<Input
				label={m.proxy_adv_wg_port()}
				type="number"
				value={wgPort}
				hint={m.proxy_adv_wg_port_hint()}
				onchange={applyWgPort}
				fullWidth
			/>
			<Dropdown
				label={m.proxy_adv_wan()}
				value={effectiveStaticWan(wdttServer)}
				options={[{ value: '', label: m.proxy_adv_wan_none() }, ...wanOptions]}
				onchange={(v) => {
					if (!wdttServer) return;
					// Выбор пользователя становится правдой целиком: одиночка
					// уезжает, список снимается (бэкенд берёт присланную форму).
					wdttServer.natStaticWan = v;
					wdttServer.natStaticWans = undefined;
				}}
				fullWidth
			/>
		</div>
		<div class="grid">
			<!-- Третий порт WG-половины: WRAP-обфускация БЕЗ слоя DTLS. Меньше
			     инкапсуляции — выше скорость, ценой потери маскировки под DTLS.
			     Поле пропало при переписывании рантайма, хотя argv его слал. -->
			<Input
				label={m.proxy_adv_direct_port()}
				type="number"
				value={directPort}
				onchange={applyDirectPort}
				placeholder={m.proxy_adv_direct_placeholder()}
				hint={m.proxy_adv_direct_hint()}
				fullWidth
			/>
			<Input label="Config dir" bind:value={wdttServer.configDir} fullWidth />
		</div>

		<div class="log-mode">
			<span class="row-label">{m.proxy_adv_log_mode()}</span>
			<SegmentedControl
				value={statsLog}
				options={statsLogOptions}
				ariaLabel={m.proxy_adv_log_mode()}
				onchange={(v) => {
					if (wdttServer) wdttServer.statsLog = v;
				}}
			/>
			<FieldHint
				text={m.proxy_adv_log_mode_hint()}
				ariaLabel={m.proxy_adv_log_mode_aria()}
			/>
		</div>
	{:else if ftServer}
		<div class="grid">
			<Input label={m.proxy_adv_backend()} bind:value={ftServer.connect} fullWidth />
			<Dropdown label={m.proxy_adv_tunnel_mode()} bind:value={ftServer.mode} options={modeOptions} fullWidth />
		</div>
	{/if}

	<KillPortSection title={m.proxy_adv_kill_ports()} {ports} />
</DetailSection>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}

	.log-mode {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.875rem;
	}

	.row-label {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		min-width: 9rem;
	}
</style>
