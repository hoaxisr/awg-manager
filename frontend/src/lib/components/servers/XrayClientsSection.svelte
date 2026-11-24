<script lang="ts">
	import { onMount } from 'svelte';
	import { m } from '$lib/i18n';
	import { Button, Toggle } from '$lib/components/ui';
	import type { XrayClient } from '$lib/types';
	import { Plus, Trash2, Copy, QrCode, Router, RotateCw, MoreHorizontal } from 'lucide-svelte';

	// Список устройств VLESS с меню действий строки. Действия (API) — у карточки сервера.
	interface Props {
		clients: XrayClient[];
		onadd: () => void;
		onshare: (client: XrayClient) => void;
		ontoggle: (client: XrayClient) => void;
		onautotunnel: (client: XrayClient) => void;
		oncopyuuid: (client: XrayClient) => void;
		onrotate: (client: XrayClient) => void;
		ondelete: (client: XrayClient) => void;
	}

	let { clients, onadd, onshare, ontoggle, onautotunnel, oncopyuuid, onrotate, ondelete }: Props = $props();

	let openMenuId = $state<string | null>(null);

	function act(fn: (client: XrayClient) => void, client: XrayClient) {
		openMenuId = null;
		fn(client);
	}

	onMount(() => {
		const handleDocClick = () => {
			openMenuId = null;
		};
		window.addEventListener('click', handleDocClick);
		return () => window.removeEventListener('click', handleDocClick);
	});
</script>

<section class="clients-section">
	<div class="section-header">
		<div class="section-title">
			<span>{m.servers_xray_devices()}</span>
			<span class="counter">({clients.length})</span>
		</div>
		<Button variant="secondary" size="sm" onclick={onadd}>
			<Plus size={14} class="mr-1" />
			{m.servers_xray_add_device()}
		</Button>
	</div>

	{#if clients.length === 0}
		<div class="empty-clients">
			<span>{m.servers_xray_devices_empty()}</span>
			<Button variant="primary" size="sm" onclick={onadd}>
				<Plus size={14} class="mr-1" />
				{m.servers_xray_add_device()}
			</Button>
		</div>
	{:else}
		<div class="clients-list">
			{#each clients as client (client.id)}
				<div class="client-item" class:client-disabled={!client.enabled}>
					<div class="client-info">
						<Toggle
							checked={client.enabled}
							onchange={() => act(ontoggle, client)}
						/>
						<span class="client-name" class:muted={!client.enabled}>{client.remark || m.servers_xray_device_fallback()}</span>
						<span class="client-status-badge" class:active={client.enabled}>
							{client.enabled ? m.servers_xray_access_allowed() : m.servers_xray_disabled()}
						</span>
					</div>

					<div class="client-actions">
						<Button variant="primary" size="sm" onclick={() => act(onshare, client)}>
							<QrCode size={14} class="mr-1" />
							{m.servers_xray_qr_link()}
						</Button>

						<div class="menu-wrap">
							<Button
								variant="secondary"
								size="sm"
								ariaLabel={m.servers_xray_device_actions()}
								onclick={(e) => {
									e.stopPropagation();
									openMenuId = openMenuId === client.id ? null : client.id;
								}}
							>
								{#snippet iconBefore()}<MoreHorizontal size={15} />{/snippet}
								{m.servers_xray_more()}
							</Button>

							{#if openMenuId === client.id}
								<!-- svelte-ignore a11y_click_events_have_key_events -->
								<!-- svelte-ignore a11y_no_static_element_interactions -->
								<div class="dropdown-menu" onclick={(e) => e.stopPropagation()}>
									<button
										type="button"
										class="dropdown-item"
										onclick={() => act(onautotunnel, client)}
									>
										<Router size={13} />
										<span>{m.servers_xray_connect_router()}</span>
									</button>
									<button
										type="button"
										class="dropdown-item"
										onclick={() => act(oncopyuuid, client)}
									>
										<Copy size={13} />
										<span>{m.servers_xray_copy_uuid()}</span>
									</button>
									<button
										type="button"
										class="dropdown-item"
										onclick={() => act(onrotate, client)}
									>
										<RotateCw size={13} />
										<span>{m.servers_xray_rotate_fp()}</span>
									</button>
									<button
										type="button"
										class="dropdown-item danger"
										onclick={() => act(ondelete, client)}
									>
										<Trash2 size={13} />
										<span>{m.servers_xray_delete_device()}</span>
									</button>
								</div>
							{/if}
						</div>
					</div>
				</div>
			{/each}
		</div>
	{/if}
</section>

<style>
	/* Clients Section */
	.clients-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.section-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
	}

	.section-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.counter {
		color: var(--color-text-muted);
		font-size: 12px;
	}

	.empty-clients {
		padding: 1.25rem;
		text-align: center;
		background: var(--color-bg-primary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		font-size: 12.5px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
	}

	.clients-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.client-item {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 8px 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		gap: 10px;
		flex-wrap: wrap;
	}

	.client-item.client-disabled {
		background: var(--color-bg-secondary);
	}

	.client-info {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 140px;
	}

	.client-name {
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.client-name.muted {
		color: var(--color-text-muted);
	}

	.client-status-badge {
		font-size: 10.5px;
		padding: 1px 7px;
		border-radius: 4px;
		background: var(--color-bg-hover);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.client-status-badge.active {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}

	.client-actions {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.menu-wrap {
		position: relative;
	}

	.dropdown-menu {
		position: absolute;
		right: 0;
		top: calc(100% + 4px);
		min-width: 205px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		box-shadow: 0 8px 20px rgba(0, 0, 0, 0.35);
		padding: 4px;
		z-index: 30;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.dropdown-item {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		padding: 7px 10px;
		background: transparent;
		border: none;
		border-radius: 4px;
		color: var(--color-text-primary);
		font-size: 12px;
		text-align: left;
		cursor: pointer;
	}
	.dropdown-item:hover {
		background: var(--color-bg-hover);
	}
	.dropdown-item.danger {
		color: var(--color-error);
	}
</style>
