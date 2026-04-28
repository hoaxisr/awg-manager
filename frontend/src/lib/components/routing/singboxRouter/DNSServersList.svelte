<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { SingboxRouterDNSServer } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import DNSServerEditModal from './DNSServerEditModal.svelte';
	import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';

	interface Props {
		servers: SingboxRouterDNSServer[];
		outboundOptions: OutboundGroup[];
		onChange: () => Promise<void> | void;
	}
	let { servers, outboundOptions, onChange }: Props = $props();

	let editTag = $state<string | null>(null);
	let addMode = $state(false);
	let deleteTag = $state<string | null>(null);
	let forceDeleteTag = $state<string | null>(null);
	let busy = $state(false);

	function detourLabel(s: SingboxRouterDNSServer): string {
		if (!s.detour) return 'default';
		return s.detour;
	}

	function detourClass(s: SingboxRouterDNSServer): string {
		if (!s.detour || s.detour === 'direct') return 'detour-direct';
		return 'detour-tunnel';
	}

	function resolverLabel(s: SingboxRouterDNSServer): string {
		if (!s.domain_resolver) return '';
		return s.domain_resolver.server;
	}

	function requestDelete(tag: string): void {
		deleteTag = tag;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteTag === null) return;
		const tag = deleteTag;
		busy = true;
		try {
			await api.singboxRouterDeleteDNSServer(tag, false);
			deleteTag = null;
			await onChange();
		} catch (e) {
			const msg = (e as Error).message;
			deleteTag = null;
			if (msg.includes('referenced')) {
				forceDeleteTag = tag;
			} else {
				notifications.error(msg);
			}
		} finally {
			busy = false;
		}
	}

	async function confirmForceDelete(): Promise<void> {
		if (forceDeleteTag === null) return;
		const tag = forceDeleteTag;
		busy = true;
		try {
			await api.singboxRouterDeleteDNSServer(tag, true);
			forceDeleteTag = null;
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			busy = false;
		}
	}
</script>

<div class="header">
	<div class="hint">{servers.length} DNS серверов</div>
	<button class="btn btn-primary" onclick={() => { addMode = true; editTag = null; }}>+ Добавить сервер</button>
</div>

{#if servers.length === 0}
	<div class="empty">
		DNS серверы не настроены. Без них правило <code>hijack-dns</code> не будет отвечать на запросы.
		Добавьте как минимум один сервер (например <code>1.1.1.1</code> UDP) чтобы DNS заработал.
	</div>
{:else}
	<div class="col-header">
		<div>Tag</div>
		<div>Type</div>
		<div>Server</div>
		<div>Detour</div>
		<div>Resolver</div>
		<div></div>
		<div></div>
	</div>

	<div class="rows">
		{#each servers as s (s.tag)}
			<div class="row">
				<div class="tag mono">{s.tag}</div>
				<span class="badge type-{s.type}">{s.type}</span>
				<div class="server mono">
					{s.server}{#if s.server_port}:{s.server_port}{/if}{#if s.path}{s.path}{/if}
				</div>
				<div class={`detour mono ${detourClass(s)}`}>{detourLabel(s)}</div>
				<div class="resolver mono">{resolverLabel(s) || '—'}</div>
				<button class="icon-btn" onclick={() => (editTag = s.tag)} aria-label="Редактировать">✎</button>
				<button class="icon-btn danger" onclick={() => requestDelete(s.tag)} aria-label="Удалить">✕</button>
			</div>
		{/each}
	</div>
{/if}

{#if addMode}
	<DNSServerEditModal
		{servers}
		{outboundOptions}
		onClose={() => (addMode = false)}
		onSave={async (server) => {
			await api.singboxRouterAddDNSServer(server);
			addMode = false;
			await onChange();
		}}
	/>
{/if}

{#if editTag !== null}
	{@const tag = editTag}
	{@const existing = servers.find((s) => s.tag === tag)}
	{#if existing}
		<DNSServerEditModal
			server={existing}
			{servers}
			{outboundOptions}
			onClose={() => (editTag = null)}
			onSave={async (server) => {
				await api.singboxRouterUpdateDNSServer(tag, server);
				editTag = null;
				await onChange();
			}}
		/>
	{/if}
{/if}

<ConfirmModal
	open={deleteTag !== null}
	title="Удалить DNS сервер"
	message={deleteTag !== null ? `Удалить DNS сервер "${deleteTag}"?` : ''}
	{busy}
	onConfirm={confirmDelete}
	onClose={() => { if (!busy) deleteTag = null; }}
/>

<ConfirmModal
	open={forceDeleteTag !== null}
	title="Удалить с потерей ссылок?"
	message="На этот DNS сервер ссылаются правила или другие серверы."
	secondary="Удалить всё равно? Зависимые правила могут перестать работать."
	confirmLabel="Удалить принудительно"
	{busy}
	onConfirm={confirmForceDelete}
	onClose={() => { if (!busy) forceDeleteTag = null; }}
/>

<style>
	.header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.hint {
		color: var(--muted-text);
		font-size: 0.85rem;
	}
	.empty {
		padding: 0.75rem 0.9rem;
		background: rgba(224, 175, 104, 0.12);
		border-left: 3px solid var(--warning, #e0af68);
		border-radius: 4px;
		color: var(--muted-text);
		font-size: 0.85rem;
		line-height: 1.5;
	}
	.empty code {
		background: var(--bg);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
		color: var(--text);
	}
	.col-header {
		display: grid;
		grid-template-columns: 130px 70px 1fr 140px 140px 24px 24px;
		gap: 0.4rem;
		padding: 0.25rem 0.75rem;
		font-size: 0.65rem;
		letter-spacing: 0.5px;
		text-transform: uppercase;
		color: var(--muted-text);
	}
	.rows {
		display: grid;
		gap: 0.2rem;
	}
	.row {
		display: grid;
		grid-template-columns: 130px 70px 1fr 140px 140px 24px 24px;
		gap: 0.4rem;
		align-items: center;
		background: var(--surface-bg);
		padding: 0.5rem 0.75rem;
		border-radius: 4px;
	}
	.mono {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
	}
	.tag {
		color: var(--text);
		font-weight: 600;
	}
	.badge {
		padding: 0.15rem 0.45rem;
		border-radius: 3px;
		font-size: 0.7rem;
		font-weight: 600;
		text-align: center;
		color: white;
	}
	.type-udp { background: var(--muted, #64748b); }
	.type-tls,
	.type-https,
	.type-quic,
	.type-h3 { background: var(--success, #22c55e); }
	.server {
		color: var(--text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.detour-direct { color: var(--muted-text); }
	.detour-tunnel { color: var(--accent, #3b82f6); }
	.resolver { color: var(--muted-text); }
	.icon-btn {
		background: transparent;
		border: none;
		color: var(--muted-text);
		cursor: pointer;
		font-size: 0.9rem;
		padding: 0.15rem;
	}
	.icon-btn.danger {
		color: var(--danger, #dc2626);
	}
</style>
