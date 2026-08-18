<script lang="ts">
	// SH-62..SH-71 — «Дополнительно» раздачи: экспертные поля WDTT-сервера,
	// режим записи server.log и освобождение портов. Свёрнута: глобального
	// режима «Эксперт» больше нет (решение Q7 ИА).
	import { FieldHint, Input, SegmentedControl } from '$lib/components/ui';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import type { WdttServerConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';
	import KillPortSection from './KillPortSection.svelte';
	import type { SharePort } from './shareConfig';

	type StatsLogMode = 'ram' | 'off' | 'disk';

	interface Props {
		/** Редактируемая копия конфига WDTT-сервера; у FreeTurn полей нет. */
		wdttServer?: WdttServerConfig;
		/** Порты инстанса — строка на каждый. */
		ports: SharePort[];
	}

	let { wdttServer = $bindable(), ports }: Props = $props();

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
				text="Запись на накопитель изнашивает память роутера. Новый режим применится при следующем запуске сервера."
				ariaLabel="Подсказка: режим server.log"
			/>
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
