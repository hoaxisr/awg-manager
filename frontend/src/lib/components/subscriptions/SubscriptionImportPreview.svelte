<script lang="ts">
	import type { SubscriptionPreviewMember } from '$lib/types';

	interface Props {
		members: SubscriptionPreviewMember[];
		excludedKeys: Set<string>;
		ontoggle: (key: string) => void;
		onselectAll: () => void;
		onselectNone: () => void;
	}
	let { members, excludedKeys, ontoggle, onselectAll, onselectNone }: Props = $props();

	let filter = $state('');
	let activeProtocol = $state<string>('');

	function protocolLabel(p: string): string {
		switch (p) {
			case 'vless': return 'VLESS';
			case 'trojan': return 'Trojan';
			case 'shadowsocks': return 'Shadowsocks';
			case 'hysteria2': return 'Hysteria2';
			case 'naive': return 'Naive';
			case 'mieru': return 'Mieru';
			default: return p;
		}
	}

	const protocolCounts = $derived.by(() => {
		const m = new Map<string, number>();
		for (const member of members) {
			m.set(member.protocol, (m.get(member.protocol) ?? 0) + 1);
		}
		return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});

	const filtered = $derived.by(() => {
		const q = filter.trim().toLowerCase();
		return members.filter((m) => {
			if (activeProtocol && m.protocol !== activeProtocol) return false;
			if (!q) return true;
			const label = (m.label ?? '').toLowerCase();
			return label.includes(q) || m.server.toLowerCase().includes(q);
		});
	});

	const keptCount = $derived(members.length - excludedKeys.size);
	const excludedCount = $derived(excludedKeys.size);
</script>

<div class="preview">
	<div class="controls">
		<input
			class="filter"
			type="text"
			bind:value={filter}
			placeholder="Фильтр по названию или серверу"
			aria-label="Фильтр серверов"
		/>
		<div class="bulk">
			<button type="button" class="link-btn" onclick={onselectAll}>Выбрать все</button>
			<span class="sep" aria-hidden="true">·</span>
			<button type="button" class="link-btn" onclick={onselectNone}>Снять все</button>
		</div>
	</div>

	{#if protocolCounts.length > 1}
		<div class="chips" role="group" aria-label="Фильтр по протоколу">
			<button
				type="button"
				class="chip"
				class:active={activeProtocol === ''}
				onclick={() => (activeProtocol = '')}
			>
				Все ({members.length})
			</button>
			{#each protocolCounts as [proto, count] (proto)}
				<button
					type="button"
					class="chip"
					class:active={activeProtocol === proto}
					onclick={() => (activeProtocol = activeProtocol === proto ? '' : proto)}
				>
					{protocolLabel(proto)} ({count})
				</button>
			{/each}
		</div>
	{/if}

	<div class="counter">
		<span class="kept">{keptCount} оставить</span>
		<span class="sep" aria-hidden="true">·</span>
		<span class="excluded">{excludedCount} исключить</span>
	</div>

	<!-- ponytail: no virtualization; add if 500+ janks -->
	<div class="list">
		{#each filtered as member (member.key)}
			{@const checked = !excludedKeys.has(member.key)}
			<label class="row" class:dropped={!checked}>
				<input
					type="checkbox"
					{checked}
					onchange={() => ontoggle(member.key)}
				/>
				<div class="main">
					<div class="name" class:empty={!member.label}>
						{member.label || `${member.server}:${member.port}`}
					</div>
					<div class="addr mono">{member.server}:{member.port}</div>
				</div>
				<div class="badges">
					<span class="badge proto">{protocolLabel(member.protocol)}</span>
					{#if member.transport && member.transport !== 'tcp'}
						<span class="badge transport">{member.transport.toUpperCase()}</span>
					{/if}
					{#if member.security === 'reality'}
						<span class="badge reality">Reality</span>
					{:else if member.security === 'tls'}
						<span class="badge tls">TLS</span>
					{/if}
				</div>
			</label>
		{:else}
			<div class="empty-list">Нет серверов по фильтру.</div>
		{/each}
	</div>
</div>

<style>
	.preview {
		display: flex;
		flex-direction: column;
		gap: 0.7rem;
	}
	.controls {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
	}
	.filter {
		flex: 1 1 200px;
		padding: 0.5rem 0.7rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		color: var(--color-text-primary);
	}
	.bulk {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		flex-shrink: 0;
	}
	.link-btn {
		padding: 0;
		border: none;
		background: transparent;
		color: var(--color-accent);
		cursor: pointer;
		font: inherit;
		font-size: 0.82rem;
	}
	.link-btn:hover {
		text-decoration: underline;
	}
	.sep {
		color: var(--color-text-muted);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
	}
	.chip {
		padding: 0.2rem 0.6rem;
		border: 1px solid var(--color-border);
		border-radius: 999px;
		background: var(--color-bg-primary);
		color: var(--color-text-muted);
		cursor: pointer;
		font: inherit;
		font-size: 0.78rem;
		transition: border-color 120ms, background 120ms, color 120ms;
	}
	.chip:hover {
		border-color: var(--color-text-muted);
	}
	.chip.active {
		border-color: var(--color-accent);
		background: rgba(88, 166, 255, 0.12);
		color: var(--color-accent);
	}

	.counter {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.8rem;
	}
	.counter .kept {
		color: var(--color-text-primary);
	}
	.counter .excluded {
		color: var(--color-text-muted);
	}

	.list {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		max-height: 50vh;
		overflow-y: auto;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		padding: 0.5rem 0.7rem;
		border: 1px solid var(--color-border);
		border-radius: 8px;
		cursor: pointer;
	}
	.row.dropped {
		opacity: 0.5;
	}
	.row.dropped .name {
		text-decoration: line-through;
	}
	.main {
		flex: 1 1 auto;
		min-width: 0;
	}
	.name {
		font-size: 0.88rem;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.name.empty {
		font-style: italic;
		color: var(--color-text-muted);
	}
	.addr {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin-top: 0.15rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.badges {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		flex-shrink: 0;
	}
	.badge {
		font-size: 0.68rem;
		padding: 0.15rem 0.5rem;
		border-radius: 4px;
		font-weight: 600;
		letter-spacing: 0.3px;
	}
	.badge.proto { background: rgba(88, 166, 255, 0.15); color: var(--color-accent); }
	.badge.transport { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
	.badge.tls { background: rgba(63, 185, 80, 0.15); color: #3fb950; }
	.badge.reality { background: rgba(210, 153, 34, 0.15); color: #d29922; }

	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}

	.empty-list {
		padding: 1rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.85rem;
	}
</style>
