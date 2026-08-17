<script lang="ts">
	// EX-34..48 — «Дополнительно»: экспертные поля, работа с WireGuard-конфигом
	// и освобождение портов. Свёрнута: глобального режима «Эксперт» больше нет
	// (решение Q7 ИА).
	import { Button, Dropdown, Input } from '$lib/components/ui';
	import WgConfExportPanel from '../proxy-panel/WgConfExportPanel.svelte';
	import type { FreeTurnClientConfig, WdttClientConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';
	import KillPortSection from './KillPortSection.svelte';

	interface Props {
		/** Редактируемая копия конфига детали — правится на месте. */
		wdttClient?: WdttClientConfig;
		ftClient?: FreeTurnClientConfig;
		/** Raw: отдельный AWG-туннель не нужен — блока WG-конфига нет (W-29). */
		raw?: boolean;
		/** WireGuard-конфиг, полученный клиентом от сервера. */
		wgConf?: string;
		/** Порты инстанса — строка на каждый. */
		ports: { listen: string; proto?: 'udp' | 'tcp' }[];
		/** Завести AWG-туннель по конфигу из журнала (ручка ensure). */
		onensuretunnel: () => Promise<void> | void;
		/** Завести AWG-туннель по конфигу, вставленному руками. */
		onimportconf: (conf: string) => Promise<void> | void;
		busyTunnel?: boolean;
	}

	let {
		wdttClient = $bindable(),
		ftClient = $bindable(),
		raw = false,
		wgConf = '',
		ports,
		onensuretunnel,
		onimportconf,
		busyTunnel = false,
	}: Props = $props();

	let confShown = $state(false);
	let manualOpen = $state(false);
	let manualConf = $state('');

	async function importManual() {
		const conf = manualConf.trim();
		if (!conf) return;
		await onimportconf(conf);
	}
</script>

<DetailSection
	title="Дополнительно"
	collapsed
	hint="Экспертные поля, ручная работа с WireGuard-конфигом и освобождение порта."
>
	<div class="grid">
		{#if wdttClient}
			<Dropdown
				label="Obfs"
				bind:value={wdttClient.obfs}
				options={[
					{ value: 'audio', label: 'audio' },
					{ value: 'video', label: 'video' },
				]}
			/>
			<Input label="Fingerprint" bind:value={wdttClient.fingerprint} fullWidth />
			<Input label="Device ID" bind:value={wdttClient.deviceId} fullWidth />
			<Input label="URL подписки" bind:value={wdttClient.sub} fullWidth />
		{:else if ftClient}
			<Input label="URL подписки" bind:value={ftClient.sub} fullWidth />
		{/if}
	</div>

	{#if !raw}
		<p class="sub-title">WireGuard-конфиг</p>
		{#if wgConf && !confShown}
			<div class="btn-row">
				<Button variant="secondary" onclick={() => (confShown = true)}>Показать</Button>
			</div>
		{:else if wgConf}
			<WgConfExportPanel
				{wgConf}
				title=""
				hint=""
				filename="wg.conf"
				onImportTunnel={onensuretunnel}
				importingTunnel={busyTunnel}
			/>
		{/if}
		<div class="btn-row">
			<Button variant="ghost" onclick={() => (manualOpen = !manualOpen)}>
				Вставить .conf вручную
			</Button>
		</div>
		{#if manualOpen}
			<textarea class="manual-conf" bind:value={manualConf} rows="8" aria-label="WireGuard-конфиг"
			></textarea>
			<div class="btn-row">
				<Button
					variant="primary"
					loading={busyTunnel}
					disabled={!manualConf.trim()}
					onclick={importManual}
				>
					Создать AWG-туннель
				</Button>
			</div>
		{/if}
	{/if}

	<KillPortSection {ports} />
</DetailSection>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}

	.sub-title {
		margin: 1.25rem 0 0.5rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.manual-conf {
		width: 100%;
		margin-top: 0.5rem;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		padding: 0.5rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		resize: vertical;
	}
</style>
