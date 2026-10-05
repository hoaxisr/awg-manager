<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { Button, Input, StatusDot } from '$lib/components/ui';
	import type { ForeignIfaceCandidate } from '$lib/types';
	import { ChevronDown, ChevronRight } from 'lucide-svelte';

	interface Props {
		onpicked: (name: string) => void;
	}
	let { onpicked }: Props = $props();

	let open = $state(false);
	let loading = $state(false);
	let busy = $state(false);
	let error = $state('');
	let name = $state('');
	let candidates = $state<ForeignIfaceCandidate[]>([]);

	async function toggle(): Promise<void> {
		open = !open;
		if (!open) return;
		loading = true;
		error = '';
		try {
			candidates = await api.listForeignIfaceCandidates();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	async function pick(target: string): Promise<void> {
		busy = true;
		error = '';
		try {
			const res = await api.markForeignIface(target);
			name = '';
			open = false;
			// Сервер пишет каноническое имя (OpkgTun7 → opkgtun7) — выбираем его.
			onpicked(res.name || target);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}
</script>

<button type="button" class="foreign-toggle" onclick={toggle}>
	{#if open}<ChevronDown size={15} strokeWidth={2.5} />{:else}<ChevronRight size={15} strokeWidth={2.5} />{/if} {m.routing_singbox_foreign_toggle()}
</button>
{#if open}
	<div class="foreign-panel">
		<p class="foreign-hint">
			{m.routing_singbox_foreign_hint()}
		</p>
		{#if loading}
			<p class="foreign-hint">{m.common_loading()}</p>
		{:else}
			{#each candidates as c (c.name)}
				<div class="foreign-row">
					<span class="foreign-name">
						<StatusDot variant={c.up ? 'success' : 'warning'} />
						{c.label}
						<span class="foreign-hint">{c.kind === 'opkgtun' ? 'OpkgTun' : m.routing_singbox_foreign_kernel()} · {c.name}</span>
					</span>
					<Button size="sm" variant="outline-primary" disabled={busy} onclick={() => pick(c.name)}>
						{m.routing_singbox_foreign_mark_label({ label: c.label })}
					</Button>
				</div>
			{/each}
		{/if}
		<div class="foreign-row">
			<Input label={m.routing_singbox_foreign_iface_label()} placeholder={m.routing_singbox_foreign_iface_placeholder()} bind:value={name} />
			<Button size="sm" variant="secondary" disabled={busy || !name.trim()} onclick={() => pick(name.trim())}>
				{m.routing_singbox_foreign_mark()}
			</Button>
		</div>
		{#if error}<div class="error-text visible">{error}</div>{/if}
	</div>
{/if}

<style>
	.foreign-toggle {
		background: none;
		border: 0;
		padding: 4px 0 0;
		color: var(--accent);
		cursor: pointer;
		font-size: 13px;
		text-align: left;
	}
	.foreign-panel {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-top: 8px;
		padding: 10px;
		border: 1px solid var(--border);
		border-radius: 8px;
	}
	.foreign-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}
	.foreign-name {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.foreign-hint {
		color: var(--text-muted);
		font-size: 12px;
		margin: 0;
	}
</style>
