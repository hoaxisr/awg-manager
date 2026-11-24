<script lang="ts">
	import { m } from '$lib/i18n';
	import type { XrayEndpointConfig, XHTTPPreset } from '$lib/types';
	import { VLESS_PRESET_LABELS, getVlessPresetLabel } from './external-access/connectionLabels';
	import { createDefaultTuning } from './xrayEndpointModel';
	import { ChevronDown, ChevronUp, Zap } from 'lucide-svelte';

	// Профиль транспорта маршрута VLESS: пресеты XHTTP, FiniteProto v2 и
	// расширенные параметры. Правит xhttp_tuning/mode переданного маршрута.
	interface Props {
		endpoint: XrayEndpointConfig;
		showAdvanced: boolean;
	}

	let { endpoint = $bindable(), showAdvanced = $bindable(false) }: Props = $props();

	function selectEndpointPreset(preset: XHTTPPreset) {
		if (endpoint.mode === 'finiteproto') {
			endpoint.mode = 'packet-up';
		}
		if (!endpoint.xhttp_tuning) {
			endpoint.xhttp_tuning = createDefaultTuning(preset);
			endpoint.uplink_method = endpoint.xhttp_tuning.uplink_method;
			return;
		}
		const t = endpoint.xhttp_tuning;
		t.preset = preset;
		if (preset === 'live_low_latency') {
			t.uplink_method = 'POST';
			t.min_posts_interval_ms = '';
			t.max_buffered_posts = 1000;
			t.sc_max_each_post_bytes = 1000000;
			t.stream_up_server_secs = '';
			t.stream_path_suffix = '';
			t.enable_xmux = false;
			t.x_padding_bytes = '100-500';
		} else if (preset === 'cdn_get') {
			t.uplink_method = 'GET';
			t.min_posts_interval_ms = '5-10';
			t.max_buffered_posts = 200;
			t.sc_max_each_post_bytes = 65536;
			t.stream_up_server_secs = '20-80';
			t.stream_path_suffix = '';
			t.enable_xmux = false;
			t.x_padding_bytes = '100-500';
		} else if (preset === 'balanced') {
			t.uplink_method = 'POST';
			t.min_posts_interval_ms = '';
			t.max_buffered_posts = 1000;
			t.sc_max_each_post_bytes = 1000000;
			t.stream_up_server_secs = '20-80';
			t.stream_path_suffix = '';
			t.enable_xmux = true;
			t.x_padding_bytes = '100-500';
		}
		endpoint.uplink_method = t.uplink_method;
	}
</script>

{#if endpoint.xhttp_tuning}
	<div class="tuning-section">
		<div class="tuning-header">
			<div class="tuning-title-group">
				<div class="tuning-title">
					<Zap size={15} class="banner-icon-warn" />
					<span>{m.servers_xray_tr_title()}</span>
				</div>
				<span class="tuning-badge font-mono">
					{#if endpoint.mode === 'finiteproto'}
						{m.servers_xray_fp_badge({ kb: Math.round((endpoint.xhttp_tuning.fp_chunk_bytes || 131072) / 1024), ways: endpoint.xhttp_tuning.fp_max_ways || 1 })}
					{:else}
						{getVlessPresetLabel(endpoint.xhttp_tuning.preset)} · {endpoint.xhttp_tuning.uplink_method}
					{/if}
				</span>
			</div>
		</div>

		<div class="preset-tabs preset-tabs-4">
			<button
				type="button"
				class="preset-tab"
				class:active={endpoint.mode !== 'finiteproto' && endpoint.xhttp_tuning.preset === 'live_low_latency'}
				onclick={() => selectEndpointPreset('live_low_latency')}
			>
				<div class="preset-name">{VLESS_PRESET_LABELS.live_low_latency.title}</div>
				<div class="preset-desc">{VLESS_PRESET_LABELS.live_low_latency.description}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpoint.mode !== 'finiteproto' && endpoint.xhttp_tuning.preset === 'cdn_get'}
				onclick={() => selectEndpointPreset('cdn_get')}
			>
				<div class="preset-name">{VLESS_PRESET_LABELS.cdn_get.title}</div>
				<div class="preset-desc">{VLESS_PRESET_LABELS.cdn_get.description}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpoint.mode !== 'finiteproto' && endpoint.xhttp_tuning.preset === 'balanced'}
				onclick={() => selectEndpointPreset('balanced')}
			>
				<div class="preset-name">{VLESS_PRESET_LABELS.balanced.title}</div>
				<div class="preset-desc">{VLESS_PRESET_LABELS.balanced.description}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpoint.mode === 'finiteproto'}
				onclick={() => {
					endpoint.mode = 'finiteproto';
					endpoint.uplink_method = 'POST';
					if (endpoint.xhttp_tuning) {
						if (!endpoint.xhttp_tuning.fp_chunk_bytes) endpoint.xhttp_tuning.fp_chunk_bytes = 131072;
						if (!endpoint.xhttp_tuning.fp_max_ways) endpoint.xhttp_tuning.fp_max_ways = 1;
					}
					showAdvanced = false;
				}}
			>
				<div class="preset-name">{m.servers_xray_tr_fp_title()}</div>
				<div class="preset-desc">{m.servers_xray_tr_fp_desc()}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpoint.mode !== 'finiteproto' && endpoint.xhttp_tuning.preset === 'custom'}
				onclick={() => {
					selectEndpointPreset('custom');
					showAdvanced = true;
				}}
			>
				<div class="preset-name">{VLESS_PRESET_LABELS.custom.title}</div>
				<div class="preset-desc">{VLESS_PRESET_LABELS.custom.description}</div>
			</button>
		</div>

		{#if endpoint.mode === 'finiteproto'}
			<div class="tuning-params">
				<div class="fp-tuning-header">
					<span class="fp-tuning-title">{m.servers_xray_tr_fp_perf_title()}</span>
					<span class="fp-tuning-sub">{m.servers_xray_tr_fp_perf_sub()}</span>
				</div>

				<div class="fp-param-block">
					<span class="field-label">{m.servers_xray_tr_chunk_label()}</span>
					<div class="preset-tabs">
						<button
							type="button"
							class="preset-tab"
							class:active={(endpoint.xhttp_tuning.fp_chunk_bytes || 131072) === 65536}
							onclick={() => {
								if (endpoint.xhttp_tuning) endpoint.xhttp_tuning.fp_chunk_bytes = 65536;
							}}
						>
							<div class="preset-name">{m.servers_xray_tr_chunk64()}</div>
							<div class="preset-desc">{m.servers_xray_tr_chunk64_desc()}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={(endpoint.xhttp_tuning.fp_chunk_bytes || 131072) === 131072}
							onclick={() => {
								if (endpoint.xhttp_tuning) endpoint.xhttp_tuning.fp_chunk_bytes = 131072;
							}}
						>
							<div class="preset-name">{m.servers_xray_tr_chunk128()}</div>
							<div class="preset-desc">{m.servers_xray_tr_chunk128_desc()}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={(endpoint.xhttp_tuning.fp_chunk_bytes || 131072) === 262144}
							onclick={() => {
								if (endpoint.xhttp_tuning) endpoint.xhttp_tuning.fp_chunk_bytes = 262144;
							}}
						>
							<div class="preset-name">{m.servers_xray_tr_chunk256()}</div>
							<div class="preset-desc">{m.servers_xray_tr_chunk256_desc()}</div>
						</button>
					</div>
				</div>

				<div class="fp-param-block">
					<span class="field-label">{m.servers_xray_tr_ways_label()}</span>
					<div class="preset-tabs preset-tabs-4">
						<button
							type="button"
							class="preset-tab"
							class:active={(endpoint.xhttp_tuning.fp_max_ways || 1) === 1}
							onclick={() => {
								if (endpoint.xhttp_tuning) endpoint.xhttp_tuning.fp_max_ways = 1;
							}}
						>
							<div class="preset-name">{m.servers_xray_tr_ways1()}</div>
							<div class="preset-desc">{m.servers_xray_tr_ways1_desc()}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={(endpoint.xhttp_tuning.fp_max_ways || 1) === 2}
							onclick={() => {
								if (endpoint.xhttp_tuning) endpoint.xhttp_tuning.fp_max_ways = 2;
							}}
						>
							<div class="preset-name">{m.servers_xray_tr_ways2()}</div>
							<div class="preset-desc">{m.servers_xray_tr_ways2_desc()}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={(endpoint.xhttp_tuning.fp_max_ways || 1) === 3}
							onclick={() => {
								if (endpoint.xhttp_tuning) endpoint.xhttp_tuning.fp_max_ways = 3;
							}}
						>
							<div class="preset-name">{m.servers_xray_tr_ways3()}</div>
							<div class="preset-desc">{m.servers_xray_tr_ways3_desc()}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={(endpoint.xhttp_tuning.fp_max_ways || 1) === 4}
							onclick={() => {
								if (endpoint.xhttp_tuning) endpoint.xhttp_tuning.fp_max_ways = 4;
							}}
						>
							<div class="preset-name">{m.servers_xray_tr_ways4()}</div>
							<div class="preset-desc">{m.servers_xray_tr_ways4_desc()}</div>
						</button>
					</div>
				</div>

				<div class="readonly-pill font-mono">
					{m.servers_xray_tr_fp_summary({ kb: Math.round((endpoint.xhttp_tuning.fp_chunk_bytes || 131072) / 1024), bytes: endpoint.xhttp_tuning.fp_chunk_bytes || 131072, ways: endpoint.xhttp_tuning.fp_max_ways || 1 })}
				</div>
			</div>
		{:else}
			<button
				type="button"
				class="advanced-toggle-btn"
				onclick={() => (showAdvanced = !showAdvanced)}
			>
				<span>{m.servers_xray_tr_advanced()}</span>
				{#if showAdvanced || endpoint.xhttp_tuning.preset === 'custom'}
					<ChevronUp size={14} />
				{:else}
					<ChevronDown size={14} />
				{/if}
			</button>

			{#if showAdvanced || endpoint.xhttp_tuning.preset === 'custom'}
				<div class="tuning-params">
					<div class="form-row">
						<label class="form-field">
							<span class="field-label">{m.servers_xray_tr_uplink_label()}</span>
							{#if endpoint.xhttp_tuning.preset === 'custom'}
								<select bind:value={endpoint.xhttp_tuning.uplink_method} class="select-input">
									<option value="POST">{m.servers_xray_tr_uplink_post()}</option>
									<option value="GET">{m.servers_xray_tr_uplink_get()}</option>
								</select>
							{:else}
								<div class="readonly-pill font-mono">{endpoint.xhttp_tuning.uplink_method} (packet-up)</div>
							{/if}
						</label>
						<label class="form-field">
							<span class="field-label">{m.servers_xray_tr_interval_label()}</span>
							{#if endpoint.xhttp_tuning.preset === 'custom'}
								<input type="text" bind:value={endpoint.xhttp_tuning.min_posts_interval_ms} placeholder={m.servers_xray_tr_interval_placeholder()} class="text-input font-mono" />
							{:else}
								<div class="readonly-pill font-mono">
									{endpoint.xhttp_tuning.min_posts_interval_ms ? m.servers_xray_tr_interval_ms({ ms: endpoint.xhttp_tuning.min_posts_interval_ms }) : m.servers_xray_tr_no_delay()}
								</div>
							{/if}
						</label>
					</div>

					<div class="form-row">
						<label class="form-field">
							<span class="field-label">{m.servers_xray_tr_xmux_label()}</span>
							{#if endpoint.xhttp_tuning.preset === 'custom'}
								<label class="checkbox-label">
									<input type="checkbox" bind:checked={endpoint.xhttp_tuning.enable_xmux} class="checkbox-input" />
									<span>{m.servers_xray_tr_xmux_enable()}</span>
								</label>
							{:else}
								<div class="readonly-pill">
									{endpoint.xhttp_tuning.enable_xmux ? m.servers_xray_tr_xmux_on() : m.servers_xray_tr_xmux_off()}
								</div>
							{/if}
						</label>
						<label class="form-field">
							<span class="field-label">{m.servers_xray_tr_padding_label()}</span>
							{#if endpoint.xhttp_tuning.preset === 'custom'}
								<input type="text" bind:value={endpoint.xhttp_tuning.x_padding_bytes} placeholder="100-500" class="text-input font-mono" />
							{:else}
								<div class="readonly-pill font-mono">
									{m.servers_xray_tr_padding_val({ bytes: endpoint.xhttp_tuning.x_padding_bytes || '100-500', header: endpoint.xhttp_tuning.x_padding_header || 'Referer' })}
								</div>
							{/if}
						</label>
					</div>
				</div>
			{/if}
		{/if}
	</div>
{/if}

<style>
	.form-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
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

	.tuning-section {
		display: flex;
		flex-direction: column;
		gap: 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 12px;
	}

	.tuning-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}

	.tuning-title-group {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.tuning-title {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.tuning-badge {
		font-size: 10px;
		padding: 2px 6px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-accent);
	}

	.preset-tabs {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 8px;
	}

	.preset-tabs-4 {
		grid-template-columns: repeat(2, 1fr);
	}

	.preset-tab {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.preset-tab:hover {
		border-color: var(--color-accent);
	}
	.preset-tab.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint, rgba(99, 102, 241, 0.08));
	}

	.preset-name {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.preset-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.tuning-params {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}

	.fp-tuning-header {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.fp-tuning-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.fp-tuning-sub {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.fp-param-block {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.advanced-toggle-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		align-self: flex-start;
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		font-size: 12px;
		cursor: pointer;
		padding: 4px 0;
	}

	.readonly-pill {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 12px;
		color: var(--color-text-primary);
	}

	.checkbox-label {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--color-text-primary);
		cursor: pointer;
		padding: 7px 0;
	}

	.checkbox-input {
		width: 16px;
		height: 16px;
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	@media (max-width: 900px) {
		.preset-tabs-4 {
			grid-template-columns: repeat(2, 1fr);
		}
	}

	@media (max-width: 640px) {
		.preset-tabs,
		.preset-tabs-4,
		.form-row {
			grid-template-columns: 1fr;
		}
	}
</style>
