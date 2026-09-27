<script lang="ts">
	import { api } from '$lib/api/client';
	import { Button, Input, StatusDot } from '$lib/components/ui';
	import type { ForeignIfaceCandidate } from '$lib/types';

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
			await api.markForeignIface(target);
			name = '';
			open = false;
			onpicked(target);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}
</script>

<button type="button" class="foreign-toggle" onclick={toggle}>
	{open ? '▾' : '▸'} Нужного нет? Интерфейс другой программы…
</button>
{#if open}
	<div class="foreign-panel">
		<p class="foreign-hint">
			Выбранный интерфейс будет отмечен как сторонний: панель не станет его удалять и занимать его номер. Для
			OpkgTun сначала создайте запись в NDMS, потом запускайте программу.
		</p>
		{#if loading}
			<p class="foreign-hint">Загрузка…</p>
		{:else}
			{#each candidates as c (c.name)}
				<div class="foreign-row">
					<span class="foreign-name">
						<StatusDot variant={c.up ? 'success' : 'warning'} />
						{c.label}
						<span class="foreign-hint">{c.kind === 'opkgtun' ? 'OpkgTun' : 'ядро'} · {c.name}</span>
					</span>
					<Button size="sm" variant="outline-primary" disabled={busy} onclick={() => pick(c.name)}>
						Отметить и выбрать «{c.label}»
					</Button>
				</div>
			{/each}
		{/if}
		<div class="foreign-row">
			<Input placeholder="имя интерфейса ядра" bind:value={name} />
			<Button size="sm" variant="secondary" disabled={busy || !name.trim()} onclick={() => pick(name.trim())}>
				Отметить и выбрать
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
