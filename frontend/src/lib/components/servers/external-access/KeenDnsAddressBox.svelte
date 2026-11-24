<script lang="ts">
	// Выбор адреса роутера в мастере внешнего доступа: существующая публикация
	// KeenDNS, новый адрес 4-го уровня или ввод вручную (snippet `manual`).
	// Один блок на обе схемы подключения: прямую ('direct') и через внешний сервис ('ext').
	import type { Snippet } from 'svelte';
	import { m } from '$lib/i18n';
	import type { KeenDNSDiscovery, KeenDNSPublication } from '$lib/types/serverWizard';
	import type { validateKeenDNSLabelClient } from './externalAccessModel';
	import { AlertTriangle, Globe, Sparkles } from 'lucide-svelte';

	type KeenDNSMode = 'manual' | 'existing' | 'create';

	interface Props {
		variant: 'direct' | 'ext';
		mode: KeenDNSMode;
		label: string;
		keendnsInfo: KeenDNSDiscovery | null;
		keendnsAvailable: boolean;
		validation: ReturnType<typeof validateKeenDNSLabelClient>;
		labelConflict: string;
		previewFqdn: string;
		publications: KeenDNSPublication[];
		onsetmode: (mode: KeenDNSMode) => void;
		onapplypublication: (label: string, fqdn: string) => void;
		onsync: () => void;
		manual: Snippet;
	}

	let {
		variant,
		mode,
		label = $bindable(),
		keendnsInfo,
		keendnsAvailable,
		validation,
		labelConflict,
		previewFqdn,
		publications,
		onsetmode,
		onapplypublication,
		onsync,
		manual,
	}: Props = $props();

	const inputId = $derived(variant === 'direct' ? 'kd-label-direct' : 'kd-label-ext');
</script>

<div class="origin-box">
	<div class="origin-header-row">
		<span class="origin-title">
			{variant === 'direct' ? m.servers_ext_wiz_kd_title_direct() : m.servers_ext_wiz_kd_title_ext()}
		</span>
		<div class="pills-row">
			{#if publications.length > 0}
				<button
					type="button"
					class="pill-btn"
					class:active={mode === 'existing'}
					onclick={() => onsetmode('existing')}
				>
					{m.servers_ext_wiz_kd_pick_existing({ count: publications.length })}
				</button>
			{/if}
			<button
				type="button"
				class="pill-btn"
				class:active={mode === 'create'}
				onclick={() => onsetmode('create')}
			>
				<Sparkles size={12} class="mr-1" />
				{m.servers_ext_wiz_kd_create()}
			</button>
			<button
				type="button"
				class="pill-btn"
				class:active={mode === 'manual'}
				onclick={() => onsetmode('manual')}
			>
				{m.servers_ext_wiz_kd_manual()}
			</button>
		</div>
	</div>

	{#if mode === 'create'}
		{#if keendnsAvailable && keendnsInfo}
			<div class="keendns-creator">
				<div class="keendns-base-row">
					<span class="field-label">{m.servers_ext_wiz_kd_base_domain()}</span>
					<code class="base-domain-pill">{keendnsInfo.domain}</code>
				</div>

				<div class="field-block">
					<label class="field-label" for={inputId}>
						{variant === 'direct' ? m.servers_ext_wiz_kd_label_direct() : m.servers_ext_wiz_kd_label_ext()}
					</label>
					<div class="keendns-input-group">
						<input
							id={inputId}
							type="text"
							class="text-input mono"
							bind:value={label}
							oninput={onsync}
							placeholder="connect"
						/>
						<span class="keendns-suffix mono">.{keendnsInfo.domain}</span>
					</div>
					{#if validation.error}
						<span class="field-error">{validation.error}</span>
					{:else if labelConflict}
						<span class="field-error">{labelConflict}</span>
					{/if}
				</div>

				<div class="keendns-preview-box">
					<Globe size={15} class="icon-info-soft" />
					<div>
						<div class="preview-line">
							{variant === 'direct' ? m.servers_ext_wiz_kd_preview_direct() : m.servers_ext_wiz_kd_preview_ext()}
							<strong class="mono">https://{previewFqdn}</strong>
						</div>
						<div class="preview-sub">
							{m.servers_ext_wiz_kd_preview_note_before()} (<code>system.configuration.save</code>){m.servers_ext_wiz_kd_preview_note_after()}
						</div>
					</div>
				</div>
			</div>
		{:else}
			<div class="keendns-unavailable-box">
				<AlertTriangle size={16} class="warn-icon" />
				<div>
					<div class="unavail-title">
						{keendnsInfo?.state === 'unsupported'
							? m.servers_ext_wiz_kd_unsupported()
							: m.servers_ext_wiz_kd_not_configured()}
					</div>
					<div class="unavail-desc">
						{variant === 'direct' ? m.servers_ext_wiz_kd_enable_direct() : m.servers_ext_wiz_kd_enable_ext()}
					</div>
				</div>
			</div>
		{/if}
	{:else if mode === 'existing'}
		<div class="existing-pubs-list">
			{#each publications as pub (pub.label)}
				<button
					type="button"
					class="choice-card compact"
					class:selected={label === pub.label}
					onclick={() => onapplypublication(pub.label, pub.fqdn)}
				>
					<div class="choice-title mono">{pub.fqdn}</div>
					<div class="choice-text">
						{m.servers_ext_wiz_kd_existing_pub({ upstream: pub.upstream })}
					</div>
				</button>
			{/each}
		</div>
	{:else}
		{@render manual()}
	{/if}
</div>

<style>
	.origin-header-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}

	.keendns-creator {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding-top: 4px;
	}

	.keendns-base-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.base-domain-pill {
		font-family: var(--font-mono);
		font-size: 12px;
		padding: 2px 8px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-accent);
	}

	.keendns-input-group {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: nowrap;
		width: 100%;
		min-width: 0;
	}

	.keendns-input-group .text-input {
		flex: 1 1 120px;
		min-width: 96px;
		width: auto;
	}

	.keendns-suffix {
		font-size: 13px;
		color: var(--color-text-secondary);
		flex: 0 1 auto;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 65%;
	}

	.keendns-preview-box {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
	}

	.preview-line {
		font-size: 12.5px;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.preview-sub {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin-top: 2px;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.keendns-unavailable-box {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
	}

	.unavail-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.unavail-desc {
		font-size: 12px;
		color: var(--color-text-secondary);
		margin-top: 2px;
	}

	.existing-pubs-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.field-error {
		font-size: 11.5px;
		color: var(--color-error);
	}

	.pills-row {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}

	.pill-btn {
		display: inline-flex;
		align-items: center;
		padding: 5px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 12px;
		cursor: pointer;
	}

	.pill-btn.active {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
		color: var(--color-accent);
		font-weight: 600;
	}

	@media (max-width: 480px) {
		.keendns-input-group {
			flex-wrap: wrap;
		}
		.keendns-suffix {
			max-width: 100%;
		}
	}
</style>
