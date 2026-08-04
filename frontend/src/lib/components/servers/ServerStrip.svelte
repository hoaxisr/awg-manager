<script lang="ts" module>
	export interface ServerItem {
		id: string;
		name: string;
		iface: string;
		listenPort: number | string;
		status: 'running' | 'stopped' | 'unknown';
		peerActive?: number;
		peerCount?: number;
		kind: 'managed' | 'system';
	}
</script>

<script lang="ts">
	// Полоса-селектор серверов (макет servers-a-toprow): управляемые и системные
	// серверы в одном ряду, различает бейдж. Заменила вертикальный рейл — на
	// full-width странице колонка в 240px съедала место у детальной карточки.
	import { Plus } from 'lucide-svelte';

	interface Props {
		items: ServerItem[];
		activeId: string;
		onSelect: (id: string) => void;
		onCreate?: () => void;
	}

	let { items, activeId, onSelect, onCreate }: Props = $props();
</script>

<nav class="strip" aria-label="Список серверов">
	{#each items as item (item.id)}
		{@const isActive = item.id === activeId}
		<button
			type="button"
			class="server"
			class:active={isActive}
			aria-current={isActive ? 'true' : undefined}
			onclick={() => onSelect(item.id)}
		>
			<span class="led led-{item.status}" aria-hidden="true"></span>
			<span class="body">
				<span class="name">{item.name}</span>
				<span class="meta">
					{item.iface}{item.listenPort ? `:${item.listenPort}` : ''}
					{#if item.peerCount !== undefined && item.peerCount > 0}
						· {item.peerActive ?? 0}/{item.peerCount} peers
					{/if}
				</span>
			</span>
			<span class="badge badge-{item.kind}">
				{item.kind === 'managed' ? 'Управляемый' : 'Встроенный'}
			</span>
		</button>
	{/each}

	{#if onCreate}
		<button type="button" class="create" onclick={onCreate}>
			<Plus size={14} strokeWidth={2} aria-hidden="true" />
			Новый сервер
		</button>
	{/if}
</nav>

<style>
	/* Sticky: старый рейл был `position: sticky` и оставался на экране при
	   прокрутке длинной таблицы клиентов — полоса обязана сохранить это
	   свойство. Приём и токены — как у .sticky-header в
	   TunnelEditHeader.svelte:112-123: top: 0, непрозрачный фон, отрицательные
	   поля, чтобы фон доходил до краёв контейнера. */
	.strip {
		position: sticky;
		top: 0;
		z-index: var(--z-sticky-secondary);
		display: flex;
		flex-wrap: wrap;
		gap: 0.625rem;
		padding: 0.75rem 1rem;
		margin: -0.75rem -1rem 0.25rem;
		background: var(--color-bg-primary);
	}

	.server {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		min-width: 240px;
		flex: 0 1 auto;
		padding: 0.625rem 1rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		font-family: inherit;
		text-align: left;
		cursor: pointer;
		transition:
			background var(--t-fast) ease,
			border-color var(--t-fast) ease;
	}

	.server:hover {
		border-color: var(--color-border-hover);
	}

	.server.active {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
	}

	.body {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.name {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.meta {
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.led-running {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success);
	}

	.led-stopped,
	.led-unknown {
		background: var(--color-text-muted);
	}

	.badge {
		margin-left: auto;
		flex-shrink: 0;
		padding: 2px 8px;
		border-radius: var(--radius-pill);
		font-size: 11px;
		font-weight: 600;
	}

	.badge-managed {
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.badge-system {
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	.create {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.625rem 1rem;
		background: transparent;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
		color: var(--color-text-muted);
		font-family: inherit;
		font-size: 12px;
		cursor: pointer;
	}

	.create:hover {
		color: var(--color-text-secondary);
		border-color: var(--color-border-hover);
	}

	@media (max-width: 640px) {
		.server,
		.create {
			min-width: 0;
			width: 100%;
		}
	}
</style>
