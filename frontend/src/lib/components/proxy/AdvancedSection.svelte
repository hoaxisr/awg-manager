<script lang="ts">
	// EX-34..48, EX-58, EX-66..EX-68 — «Дополнительно»: экспертные поля, работа
	// с WireGuard-конфигом и освобождение портов. Свёрнута: глобального режима
	// «Эксперт» больше нет (решение Q7 ИА).
	import { m } from '$lib/i18n';
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
	title={m.proxy_adv_title()}
	collapsed
	hint={m.proxy_adv_client_hint()}
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
				label={m.proxy_adv_vk_auth()}
				bind:value={wdttClient.vkAuthMode}
				options={vkAuthOptions}
				fullWidth
			/>
			<Input label={m.proxy_adv_sub_url()} bind:value={wdttClient.sub} fullWidth />
		{:else if ftClient}
			<Input label="Provider" bind:value={ftClient.provider} fullWidth />
			<Input label="Client ID" bind:value={ftClient.clientId} fullWidth />
			<SensitiveInput label={m.proxy_adv_obf_key()} bind:value={ftClient.obfKey} />
			<Dropdown
				label={m.proxy_adv_obf_profile()}
				bind:value={ftClient.obfProfile}
				options={obfOptions}
				fullWidth
			/>
			<Input
				label={m.proxy_adv_obf_timing()}
				type="number"
				hint={m.proxy_adv_obf_timing_hint()}
				value={String(ftClient.obfTimingMs)}
				onchange={(v) => {
					// 0 — законное «выкл.», шаблон `Number(v) || прежнее` его не выставит.
					if (ftClient) ftClient.obfTimingMs = Math.max(0, Math.trunc(Number(v)) || 0);
				}}
				fullWidth
			/>
			<Input label={m.proxy_adv_sub_url()} bind:value={ftClient.sub} fullWidth />
		{/if}
	</div>

	{#if ftClient}
		<div class="toggle-row">
			<Toggle
				label="Bond"
				hint={m.proxy_adv_bond_hint()}
				checked={ftClient.bond}
				disabled={ftClient.mode !== 'tcp'}
				onchange={(v) => {
					if (ftClient) ftClient.bond = v;
				}}
			/>
		</div>
	{/if}

	{#if !raw}
		<p class="sub-title">{m.proxy_adv_wg_conf()}</p>
		{#if wgConf && !confShown}
			<div class="btn-row">
				<Button variant="secondary" onclick={() => (confShown = true)}>{m.common_show()}</Button>
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
		<ConfPasteBox label={m.proxy_adv_paste_conf()} bind:value={manualConf}>
			<div class="btn-row">
				<Button
					variant="primary"
					loading={busyTunnel}
					disabled={!manualConf.trim()}
					onclick={importManual}
				>
					{m.proxy_adv_create_tunnel()}
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
