<script lang="ts">
	import { singbox } from '$lib/stores/singbox';
	import { api } from '$lib/api/client';

	const { status } = singbox;

	let installing = $state(false);
	let error = $state<string | null>(null);

	async function install(): Promise<void> {
		installing = true;
		error = null;
		try {
			await api.singboxInstall();
			await singbox.loadStatus();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			installing = false;
		}
	}
</script>

{#if $status && !$status.installed}
	<div class="banner">
		<div class="text">
			<strong>Sing-box не установлен</strong>
			<span>Установите для поддержки VLESS/Reality, Hysteria2, NaiveProxy</span>
		</div>
		<button class="btn btn-primary btn-sm" onclick={install} disabled={installing}>
			{installing ? 'Установка...' : 'Установить'}
		</button>
		{#if error}
			<div class="error">{error}</div>
		{/if}
	</div>
{/if}

<style>
	.banner {
		display: flex;
		align-items: center;
		gap: 1rem;
		padding: 1rem;
		border: 1px solid var(--warning);
		background: rgba(245, 158, 11, 0.08);
		border-radius: var(--radius);
		margin-bottom: 1rem;
	}
	.text { flex: 1; display: flex; flex-direction: column; gap: 4px; }
	.error { color: var(--error); font-size: 12px; }
</style>
