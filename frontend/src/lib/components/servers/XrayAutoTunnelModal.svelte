<script lang="ts">
	import { untrack } from 'svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { XrayShareLinks, XrayEndpointConfig } from '$lib/types';
	import { Copy, Router, Network } from 'lucide-svelte';

	interface Props {
		open: boolean;
		clientId: string;
		clientRemark: string;
		endpoints?: XrayEndpointConfig[];
		onclose: () => void;
	}

	let { open = $bindable(false), clientId, clientRemark, endpoints = [], onclose }: Props = $props();

	let links = $state<XrayShareLinks | null>(null);
	let loading = $state(false);
	let targetType = $state<'remote_router' | 'local'>('remote_router');
	let transportMode = $state<'finiteproto' | 'xhttp'>('finiteproto');
	let selectedEndpointId = $state('');
	let initializedForOpen = false;

	let activeEndpoints = $derived(
		(endpoints || []).filter((ep) => ep.enabled && ep.public_domain)
	);

	let activeUrl = $derived(
		transportMode === 'finiteproto' && links?.finiteproto_url
			? links.finiteproto_url
			: links?.vless_url || ''
	);

	$effect(() => {
		if (open) {
			if (!initializedForOpen && clientId) {
				initializedForOpen = true;
				untrack(() => {
					const defaultEp = activeEndpoints[0]?.id || '';
					selectedEndpointId = defaultEp;
					void loadLinks(defaultEp, true);
				});
			}
		} else {
			initializedForOpen = false;
			links = null;
			selectedEndpointId = '';
		}
	});

	async function loadLinks(endpointId?: string, initial = false) {
		if (initial) loading = true;
		try {
			const ep = activeEndpoints.find((e) => e.id === (endpointId ?? selectedEndpointId));
			const dom = ep?.public_domain?.split(',')[0]?.trim();
			links = await api.getXrayClientLinks(clientId, dom || undefined, ep?.id || undefined);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_data_load_failed());
		} finally {
			if (initial) loading = false;
		}
	}

	async function selectEndpoint(epId: string) {
		selectedEndpointId = epId;
		await loadLinks(epId, false);
	}

	async function copyLink() {
		if (!activeUrl) return;
		if (await copyToClipboard(activeUrl)) {
			notifications.success(
				transportMode === 'finiteproto'
					? m.servers_xray_fp_link_copied()
					: m.servers_xray_xhttp_link_copied()
			);
		}
	}
</script>

<Modal bind:open title={m.servers_xray_autotunnel_title({ name: clientRemark || m.servers_xray_client_fallback() })} {onclose} size="md">
	{#if loading}
		<div class="loading-box">{m.servers_xray_loading_data()}</div>
	{:else if links}
		<div class="modal-body">
			{#if activeEndpoints.length > 1}
				<div class="route-picker">
					<span class="route-picker-label">{m.servers_xray_vless_route_label()}</span>
					<div class="route-pills">
						{#each activeEndpoints as ep (ep.id)}
							<button
								type="button"
								class="route-pill"
								class:active={selectedEndpointId === ep.id}
								onclick={() => void selectEndpoint(ep.id)}
							>
								<span class="route-pill-title">{ep.name || ep.public_domain}</span>
								<span class="route-pill-sub font-mono">{ep.public_domain}</span>
							</button>
						{/each}
					</div>
				</div>
			{/if}

			<div class="target-selector">
				<button
					type="button"
					class="target-card"
					class:active={targetType === 'remote_router'}
					onclick={() => (targetType = 'remote_router')}
				>
					<div class="target-icon">
						<Router size={20} />
					</div>
					<div class="target-info">
						<div class="target-title">{m.servers_xray_target_router_title()}</div>
						<div class="target-desc">{m.servers_xray_target_router_desc()}</div>
					</div>
				</button>

				<button
					type="button"
					class="target-card"
					class:active={targetType === 'local'}
					onclick={() => (targetType = 'local')}
				>
					<div class="target-icon">
						<Network size={20} />
					</div>
					<div class="target-info">
						<div class="target-title">{m.servers_xray_target_local_title()}</div>
						<div class="target-desc">{m.servers_xray_target_local_desc()}</div>
					</div>
				</button>
			</div>

			{#if links.finiteproto_url}
				<div class="route-picker">
					<span class="route-picker-label">{m.servers_xray_tunnel_transport_label()}</span>
					<div class="route-pills">
						<button
							type="button"
							class="route-pill"
							class:active={transportMode === 'finiteproto'}
							onclick={() => (transportMode = 'finiteproto')}
						>
							<span class="route-pill-title">{m.servers_xray_fp_pill_title()}</span>
							<span class="route-pill-sub font-mono">{m.servers_xray_fp_pill_sub()}</span>
						</button>
						<button
							type="button"
							class="route-pill"
							class:active={transportMode === 'xhttp'}
							onclick={() => (transportMode = 'xhttp')}
						>
							<span class="route-pill-title">VLESS XHTTP (packet-up)</span>
							<span class="route-pill-sub font-mono">{m.servers_xray_xhttp_pill_sub()}</span>
						</button>
					</div>
				</div>
			{/if}

			{#if targetType === 'remote_router'}
				<div class="guide-box">
					<div class="guide-step">
						<span class="step-num">1</span>
						<span>{m.servers_xray_autotunnel_step1({ transport: transportMode === 'finiteproto' ? 'FiniteProto' : 'XHTTP' })}</span>
					</div>
					<div class="copy-box">
						<div class="vless-url-box">{activeUrl}</div>
						<Button variant="primary" size="sm" onclick={copyLink}>
							<Copy size={14} class="mr-1" />
							{m.common_copy()}
						</Button>
					</div>

					<div class="guide-step mt-3">
						<span class="step-num">2</span>
						<span>{m.servers_xray_autotunnel_step2()}</span>
					</div>
					<div class="guide-instruction">
						{m.servers_xray_autotunnel_import_go()}<strong>{m.servers_xray_autotunnel_import_tunnels()}</strong>{m.servers_xray_autotunnel_import_press()}<strong>{m.servers_xray_autotunnel_import_add()}</strong>{m.servers_xray_autotunnel_import_choose()}<strong>{m.servers_xray_autotunnel_import_by_link()}</strong>{m.servers_xray_autotunnel_import_paste()}
					</div>

					<div class="note-box">
						{#if transportMode === 'finiteproto'}
							{m.servers_xray_autotunnel_fp_note_before()}<strong>FiniteProto (FP01)</strong>{m.servers_xray_autotunnel_fp_note_mid()}<code>awgm-fpc</code>{m.servers_xray_autotunnel_fp_note_after()}
						{:else}
							{m.servers_xray_autotunnel_xhttp_note()}
						{/if}
					</div>
				</div>
			{:else}
				<div class="guide-box">
					<div class="guide-instruction">
						{m.servers_xray_autotunnel_local_intro()}
					</div>
					<div class="copy-box">
						<div class="vless-url-box">{activeUrl}</div>
						<Button variant="primary" size="sm" onclick={copyLink}>
							<Copy size={14} class="mr-1" />
							{m.common_copy()}
						</Button>
					</div>
					<div class="guide-instruction mt-2">
						{m.servers_xray_autotunnel_local_go()}<strong>{m.servers_xray_autotunnel_local_subs()}</strong>{m.servers_xray_autotunnel_local_or()}<strong>{m.servers_xray_autotunnel_import_tunnels()}</strong>{m.servers_xray_autotunnel_local_add()}
					</div>
				</div>
			{/if}
		</div>
	{:else}
		<div class="error-box">{m.servers_xray_autotunnel_failed()}</div>
	{/if}

	{#snippet actions()}
		<Button variant="secondary" onclick={onclose}>{m.common_close()}</Button>
	{/snippet}
</Modal>

<style>
	.loading-box, .error-box {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 14px;
	}

	.modal-body {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.route-picker {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.route-picker-label {
		font-size: 11px;
		font-weight: 600;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.route-pills {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: 8px;
	}

	.route-pill {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 8px 10px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}
	.route-pill:hover {
		border-color: var(--color-accent);
	}
	.route-pill.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.route-pill-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.route-pill-sub {
		font-size: 11px;
		color: var(--color-text-muted);
	}
	.font-mono {
		font-family: var(--font-mono);
	}

	.target-selector {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.target-card {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		cursor: pointer;
		text-align: left;
		transition: all var(--t-fast) ease;
	}

	.target-card:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-accent-border);
	}

	.target-card.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.target-icon {
		color: var(--color-accent);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.target-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.target-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		margin-top: 2px;
	}

	.guide-box {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 14px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.guide-step {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.step-num {
		width: 20px;
		height: 20px;
		border-radius: 50%;
		background: var(--color-accent);
		color: #ffffff;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		font-size: 11px;
		font-weight: bold;
		flex-shrink: 0;
	}

	.copy-box {
		display: flex;
		flex-direction: column;
		gap: 8px;
		align-items: flex-end;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 10px 12px;
	}

	.vless-url-box {
		width: 100%;
		font-family: var(--font-mono);
		font-size: 11px;
		line-height: 1.5;
		color: var(--color-text-primary);
		word-break: break-all;
		white-space: pre-wrap;
		user-select: all;
	}

	.guide-instruction {
		font-size: 12px;
		color: var(--color-text-muted);
		line-height: 1.5;
	}

	.note-box {
		margin-top: 8px;
		padding: 8px 10px;
		background: var(--color-bg-secondary);
		border-left: 3px solid var(--color-accent);
		border-radius: var(--radius-sm);
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.4;
	}

	.mt-3 {
		margin-top: 0.75rem;
	}

	.mt-2 {
		margin-top: 0.5rem;
	}
</style>
