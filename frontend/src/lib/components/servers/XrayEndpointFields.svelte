<script lang="ts">
	import { m } from '$lib/i18n';
	import type { XrayEndpointConfig } from '$lib/types';
	import type { KeenDNSDiscovery } from '$lib/types/serverWizard';

	// Поля маршрута CDN/KeenDNS: имя, домен, источник (Origin), путь, порт и режим.
	interface Props {
		endpoint: XrayEndpointConfig;
		keendnsDiscovery: KeenDNSDiscovery | null;
	}

	let { endpoint = $bindable(), keendnsDiscovery }: Props = $props();
</script>

<div class="form-row">
	<label class="form-field">
		<span class="field-label">{m.servers_xray_ep_name_label()}</span>
		<input
			type="text"
			bind:value={endpoint.name}
			placeholder={m.servers_xray_ep_name_placeholder()}
			class="text-input"
		/>
	</label>
	<label class="form-field">
		<span class="field-label">
			{endpoint.connection_way === 'external_https'
				? m.servers_xray_ep_cdn_domain_label()
				: m.servers_xray_ep_keendns_domain_label()}
		</span>
		<input
			type="text"
			bind:value={endpoint.public_domain}
			placeholder={endpoint.connection_way === 'external_https'
				? 'cdn.example.ru'
				: 'vless.example.keenetic.pro'}
			class="text-input font-mono"
		/>
	</label>
</div>

{#if keendnsDiscovery?.publications && keendnsDiscovery.publications.length > 0}
	<div class="keendns-Chips-row">
		<span class="picker-chip-label">
			{endpoint.connection_way === 'external_https'
				? m.servers_xray_ep_pick_origin()
				: m.servers_xray_ep_pick_keendns()}
		</span>
		<div class="keendns-chips">
			{#each keendnsDiscovery.publications as pub (pub.fqdn)}
				<button
					type="button"
					class="keendns-chip font-mono"
					class:active={endpoint.connection_way === 'external_https'
						? endpoint.origin_host === pub.fqdn
						: endpoint.public_domain === pub.fqdn}
					onclick={() => {
						if (endpoint.connection_way === 'external_https') {
							endpoint.origin_host = pub.fqdn;
						} else {
							endpoint.public_domain = pub.fqdn;
						}
					}}
				>
					{pub.fqdn}
				</button>
			{/each}
		</div>
	</div>
{/if}

{#if endpoint.connection_way === 'external_https'}
	<div class="form-row">
		<label class="form-field">
			<span class="field-label">{m.servers_xray_ep_origin_label()}</span>
			<input
				type="text"
				bind:value={endpoint.origin_host}
				placeholder="vless.example.keenetic.pro"
				class="text-input font-mono"
			/>
		</label>
		<div class="form-field">
			<span class="field-label">{m.servers_xray_ep_cdn_params_label()}</span>
			<div class="readonly-pill font-mono">
				{m.servers_xray_ep_cdn_params({ origin: endpoint.origin_host || m.servers_xray_ep_keendns_placeholder(), path: (endpoint.path || '/cdn-bridge/').replace(/\/+$/, '') })}
			</div>
		</div>
	</div>
{/if}

<div class="form-row">
	<label class="form-field">
		<span class="field-label">{m.servers_xray_ep_path_label()}</span>
		<input
			type="text"
			bind:value={endpoint.path}
			placeholder="/cdn-bridge/"
			class="text-input font-mono"
		/>
	</label>
	<label class="form-field">
		<span class="field-label">{m.servers_xray_ep_port_mode_label()}</span>
		<div class="inline-inputs">
			<input
				type="number"
				bind:value={endpoint.public_port}
				placeholder="443"
				class="text-input font-mono"
			/>
			<select bind:value={endpoint.mode} class="select-input">
				<option value="packet-up">packet-up (XHTTP)</option>
				<option value="stream-up">stream-up (XHTTP)</option>
				<option value="stream-one">stream-one (XHTTP)</option>
				<option value="auto">auto (XHTTP)</option>
				<option value="finiteproto">finiteproto (FiniteProto v2)</option>
			</select>
		</div>
	</label>
</div>

<style>
	.keendns-Chips-row {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 10px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.picker-chip-label {
		font-size: 11.5px;
		color: var(--color-text-muted);
	}

	.keendns-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.keendns-chip {
		padding: 4px 10px;
		font-size: 11.5px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}
	.keendns-chip:hover {
		border-color: var(--color-accent);
		color: var(--color-text-primary);
	}
	.keendns-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint, rgba(99, 102, 241, 0.12));
		color: var(--color-accent);
	}

	.form-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}

	.inline-inputs {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		gap: 4px;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.text-input,
	.select-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
		outline: none;
	}
	.text-input:focus,
	.select-input:focus {
		border-color: var(--color-accent);
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	.readonly-pill {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 12px;
		color: var(--color-text-primary);
	}

	@media (max-width: 640px) {
		.form-row,
		.inline-inputs {
			grid-template-columns: 1fr;
		}
	}
</style>
