<script lang="ts">
	// EX-15..24, EX-57, EX-59..EX-65 — «Параметры» клиента. Поля правятся в
	// конфиге инстанса на месте; сохраняет и откатывает страница (владелец
	// конфига).
	import { m } from '$lib/i18n';
	import { Button, Dropdown, FormRow, Input, SegmentedControl } from '$lib/components/ui';
	import { setPeer, switchConnMode } from '$lib/utils/wdttPeerMode';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import { dnsModeOptions, modeOptions, platformOptions, transportOptions } from '../freeturn/options';
	import type { FreeTurnClientConfig, WdttClientConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';

	interface Props {
		/** Редактируемая копия конфига детали — правится на месте. */
		wdttClient?: WdttClientConfig;
		ftClient?: FreeTurnClientConfig;
		saving?: boolean;
		/** Непусто — сохранять нечего: конфиг заведомо не заработает. */
		saveBlockedHint?: string;
		onsave: () => void;
		onrevert: () => void;
	}

	let {
		wdttClient = $bindable(),
		ftClient = $bindable(),
		saving = false,
		saveBlockedHint = '',
		onsave,
		onrevert,
	}: Props = $props();

	// Режим подключения к серверу. Раньше он приезжал ТОЛЬКО из импортируемой
	// ссылки, и сменить его в UI было нечем — при живом бейдже режима в списке.
	const connModeOptions = [
		{ value: 'wg' as const, label: 'WG' },
		{ value: 'raw' as const, label: 'Raw' },
	];

	// -captcha-mode: auto|rjs|wv, дефолт роутера rjs (internal/wdtt/types.go:24).
	const captchaOptions = $derived([
		{ value: 'rjs', label: m.proxy_exit_params_captcha_rjs() },
		{ value: 'auto', label: 'auto' },
		{ value: 'wv', label: 'wv' },
	]);
</script>

<DetailSection title={m.proxy_exit_params_title()}>
	{#if wdttClient}
		<!-- Одна сетка «метка — контрол» на всю секцию (решение по вёрстке
		     2026-08-27): метки в колонке, ширина поля по содержимому. -->
		<div class="form">
			<FormRow
				label={m.proxy_exit_params_conn_mode()}
				hint={m.proxy_exit_params_conn_mode_hint()}
			>
				<SegmentedControl
					value={wdttClient.connMode === 'raw' ? 'raw' : 'wg'}
					options={connModeOptions}
					ariaLabel={m.proxy_exit_params_conn_mode()}
					onchange={(v) => {
						if (wdttClient) switchConnMode(wdttClient, v);
					}}
				/>
			</FormRow>

			<FormRow
				label={m.proxy_exit_params_server_address()}
				for="exit-peer"
				hint={m.proxy_exit_params_peer_hint()}
			>
				<Input
					id="exit-peer"
					value={wdttClient.peer}
					oninput={(v) => setPeer(wdttClient, v)}
					fullWidth
				/>
			</FormRow>

			<FormRow label={m.proxy_common_password()} hint={m.proxy_exit_params_applied_on_restart()}>
				<SensitiveInput bind:value={wdttClient.password} />
			</FormRow>

			<FormRow label={m.proxy_exit_params_vk_hashes()} for="exit-vk" hint={m.proxy_exit_params_applied_on_restart()}>
				<Input id="exit-vk" bind:value={wdttClient.vkHashes} fullWidth />
			</FormRow>

			<FormRow label={m.proxy_exit_params_workers()} for="exit-workers">
				<div class="w-num">
					<Input
						id="exit-workers"
						type="number"
						value={String(wdttClient.workers)}
						onchange={(v) => (wdttClient.workers = Number(v) || wdttClient.workers)}
						fullWidth
					/>
				</div>
			</FormRow>

			<FormRow label={m.proxy_exit_params_captcha_mode()}>
				<div class="w-select">
					<Dropdown bind:value={wdttClient.captchaMode} options={captchaOptions} fullWidth />
				</div>
			</FormRow>
		</div>
	{:else if ftClient}
		<div class="grid">
			<Input label={m.proxy_exit_params_server_address()} bind:value={ftClient.peer} fullWidth />
			<Input label={m.proxy_exit_params_vk_links()} bind:value={ftClient.links} fullWidth />
			<Input
				label={m.proxy_exit_params_workers()}
				type="number"
				value={String(ftClient.streams)}
				onchange={(v) => (ftClient.streams = Number(v) || ftClient.streams)}
				fullWidth
			/>
			<Input
				label={m.proxy_exit_params_streams_per_cred()}
				type="number"
				value={String(ftClient.streamsPerCred)}
				onchange={(v) => (ftClient.streamsPerCred = Number(v) || ftClient.streamsPerCred)}
				fullWidth
			/>
			<Dropdown label={m.proxy_exit_source_mode()} bind:value={ftClient.mode} options={modeOptions} fullWidth />
			<Dropdown
				label={m.proxy_exit_params_transport()}
				bind:value={ftClient.transport}
				options={transportOptions}
				fullWidth
			/>
			<Dropdown
				label={m.proxy_exit_params_platform()}
				bind:value={ftClient.platform}
				options={platformOptions()}
				fullWidth
			/>
			<Dropdown label={m.proxy_exit_params_dns_mode()} bind:value={ftClient.dnsMode} options={dnsModeOptions()} fullWidth />
			<Input label={m.proxy_exit_params_dns_servers()} bind:value={ftClient.dnsServers} fullWidth />
		</div>
	{/if}
	<div class="btn-row">
		<Button variant="primary" loading={saving} disabled={!!saveBlockedHint} onclick={onsave}>
			{m.common_save()}
		</Button>
		<Button variant="ghost" onclick={onrevert}>{m.proxy_common_cancel_action()}</Button>
		{#if saveBlockedHint}
			<span class="save-blocked">{saveBlockedHint}</span>
		{/if}
	</div>
</DetailSection>

<style>
	.save-blocked {
		font-size: 12px;
		color: var(--color-warning-text, var(--color-text-secondary));
		align-self: center;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}

	/* Сетка формы WDTT-клиента: у FreeTurn полей вдвое больше и они
	   однотипные — там сетка карточек читается лучше строчной. */
	.form {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		--form-label-w: 150px;
	}

	.form :global(.form-row-control > *) {
		max-width: 420px;
	}

	.form :global(.form-row-control > [role='group']) {
		width: fit-content;
	}

	.w-num {
		width: 96px;
	}

	.w-select {
		width: 220px;
	}




</style>
