<script lang="ts">
	// SH-62..SH-71, SH-84/SH-85 — «Дополнительно» раздачи: экспертные поля
	// WDTT-сервера, бэкенд и режим туннеля FreeTurn-сервера, режим записи
	// server.log и освобождение портов. Свёрнута: глобального режима «Эксперт»
	// больше нет (решение Q7 ИА).
	import { Dropdown, FieldHint, Input, SegmentedControl } from '$lib/components/ui';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import { modeOptions } from '../freeturn/options';
	import { listenPortNumber, setListenPort } from '$lib/utils/listenPortUtils';
	import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';
	import KillPortSection from './KillPortSection.svelte';
	import type { SharePort } from './shareConfig';

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

	// Порт DTLS — главный порт раздачи и обязательное поле конфига
	// (`WdttServerConfig.Validate`). Раньше правился только в мастере.
	const dtlsPort = $derived(String(listenPortNumber(wdttServer?.listen ?? '', 0) || 56002));

	function applyDtlsPort(v: string) {
		const port = Number(v);
		if (!wdttServer || !Number.isFinite(port) || port <= 0) return;
		wdttServer.listen = setListenPort(wdttServer.listen || '0.0.0.0:56002', port, '0.0.0.0');
	}

	const statsLogOptions: { value: StatsLogMode; label: string }[] = [
		{ value: 'ram', label: 'RAM' },
		{ value: 'off', label: 'Выкл' },
		{ value: 'disk', label: 'Flash' },
	];

	const statsLog = $derived((wdttServer?.statsLog?.trim() || 'ram') as StatsLogMode);
</script>

<DetailSection title="Дополнительно" collapsed hint="Экспертные поля и освобождение портов.">
	{#if wdttServer}
		<div class="grid">
			<Input
				label="Порт DTLS"
				type="number"
				value={dtlsPort}
				hint="Главный порт раздачи; raw-половина займёт следующий. Смена перезапустит сервер"
				onchange={applyDtlsPort}
				fullWidth
			/>
			<Dropdown
				label="Выход в интернет"
				value={wdttServer.natStaticWan ?? ''}
				options={[{ value: '', label: 'Не выбран' }, ...wanOptions]}
				onchange={(v) => {
					if (wdttServer) wdttServer.natStaticWan = v;
				}}
				fullWidth
			/>
		</div>
		<div class="grid">
			<Input label="Config dir" bind:value={wdttServer.configDir} fullWidth />
			<Input label="Admin ID" bind:value={wdttServer.adminId} fullWidth />
			<SensitiveInput label="Bot token" bind:value={wdttServer.botToken} />
		</div>

		<div class="log-mode">
			<span class="row-label">Режим server.log</span>
			<SegmentedControl
				value={statsLog}
				options={statsLogOptions}
				ariaLabel="Режим server.log"
				onchange={(v) => {
					if (wdttServer) wdttServer.statsLog = v;
				}}
			/>
			<FieldHint
				text="Запись на накопитель изнашивает память роутера. Запущенный сервер будет перезапущен."
				ariaLabel="Подсказка: режим server.log"
			/>
		</div>
	{:else if ftServer}
		<div class="grid">
			<Input label="Бэкенд (connect)" bind:value={ftServer.connect} fullWidth />
			<Dropdown label="Режим туннеля" bind:value={ftServer.mode} options={modeOptions} fullWidth />
		</div>
	{/if}

	<KillPortSection title="Освобождение портов" {ports} />
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
