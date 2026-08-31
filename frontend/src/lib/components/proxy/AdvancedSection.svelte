<script lang="ts">
	// EX-34..48, EX-58, EX-66..EX-68 — «Дополнительно»: экспертные поля, работа
	// с WireGuard-конфигом и освобождение портов. Свёрнута: глобального режима
	// «Эксперт» больше нет (решение Q7 ИА).
	import { Button, Dropdown, Input, Toggle } from '$lib/components/ui';
	import WgConfExportPanel from '../proxy-panel/WgConfExportPanel.svelte';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import { obfOptions } from '../freeturn/options';
	import type { FreeTurnClientConfig, WdttClientConfig } from '$lib/types';
	import ConfPasteBox from './ConfPasteBox.svelte';
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
	let manualConf = $state('');

	// -vk-auth-mode: маппинг awg-manager → wt-client (internal/wdtt/service.go:951).
	const vkAuthOptions = [
		{ value: 'vkcalls', label: 'vkcalls' },
		{ value: 'anonymous', label: 'anonymous' },
		{ value: 'account', label: 'account' },
	];

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
			<Dropdown
				label="VK-авторизация"
				bind:value={wdttClient.vkAuthMode}
				options={vkAuthOptions}
				fullWidth
			/>
			<Input label="URL подписки" bind:value={wdttClient.sub} fullWidth />
		{:else if ftClient}
			<Input label="Provider" bind:value={ftClient.provider} fullWidth />
			<Input label="Client ID" bind:value={ftClient.clientId} fullWidth />
			<SensitiveInput label="Ключ обфускации" bind:value={ftClient.obfKey} />
			<Dropdown
				label="Профиль обфускации"
				bind:value={ftClient.obfProfile}
				options={obfOptions}
				fullWidth
			/>
			<Input label="URL подписки" bind:value={ftClient.sub} fullWidth />
		{/if}
	</div>

	{#if ftClient}
		<div class="toggle-row">
			<Toggle
				label="Bond"
				hint="Bond — только в режиме TCP"
				checked={ftClient.bond}
				onchange={(v) => {
					if (ftClient) ftClient.bond = v;
				}}
			/>
		</div>
	{/if}

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
		<ConfPasteBox label="Вставить .conf вручную" bind:value={manualConf}>
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
		</ConfPasteBox>
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

	.toggle-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.875rem;
	}
</style>
