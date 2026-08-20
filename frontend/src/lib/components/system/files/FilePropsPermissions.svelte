<script lang="ts">
	import { api, type SystemFileEntry } from '$lib/api/client';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { Shield } from 'lucide-svelte';

	interface Props {
		entry: SystemFileEntry;
		readOnly?: boolean;
		onUpdated?: () => void;
	}

	let { entry, readOnly = false, onUpdated }: Props = $props();

	let uR = $state(true);
	let uW = $state(true);
	let uX = $state(false);
	let gR = $state(true);
	let gW = $state(false);
	let gX = $state(false);
	let oR = $state(true);
	let oW = $state(false);
	let oX = $state(false);
	let savingChmod = $state(false);

	$effect(() => {
		parseMode(entry.mode);
	});

	function parseMode(m: string) {
		if (!m) return;
		if (m.length >= 9) {
			const str = m.slice(-9);
			uR = str[0] === 'r';
			uW = str[1] === 'w';
			uX = str[2] === 'x';
			gR = str[3] === 'r';
			gW = str[4] === 'w';
			gX = str[5] === 'x';
			oR = str[6] === 'r';
			oW = str[7] === 'w';
			oX = str[8] === 'x';
		}
	}

	const octalMode = $derived.by(() => {
		const u = (uR ? 4 : 0) + (uW ? 2 : 0) + (uX ? 1 : 0);
		const g = (gR ? 4 : 0) + (gW ? 2 : 0) + (gX ? 1 : 0);
		const o = (oR ? 4 : 0) + (oW ? 2 : 0) + (oX ? 1 : 0);
		return `0${u}${g}${o}`;
	});

	async function applyChmod() {
		if (readOnly) return;
		savingChmod = true;
		try {
			const rawOctal = octalMode.replace(/^0/, '');
			await api.systemFilesChmod(entry.path, rawOctal);
			notifications.success(`Права изменены на ${rawOctal}`);
			onUpdated?.();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось изменить права'));
		} finally {
			savingChmod = false;
		}
	}
</script>

<div class="section-box">
	<div class="section-title">
		<Shield size={15} />
		<span>Права доступа (chmod {octalMode})</span>
	</div>

	<div class="chmod-table">
		<div class="chmod-header">
			<span></span>
			<span>Чтение (r)</span>
			<span>Запись (w)</span>
			<span>Запуск (x)</span>
		</div>
		<div class="chmod-row">
			<span class="role">Владелец:</span>
			<label><input type="checkbox" bind:checked={uR} disabled={readOnly} /></label>
			<label><input type="checkbox" bind:checked={uW} disabled={readOnly} /></label>
			<label><input type="checkbox" bind:checked={uX} disabled={readOnly} /></label>
		</div>
		<div class="chmod-row">
			<span class="role">Группа:</span>
			<label><input type="checkbox" bind:checked={gR} disabled={readOnly} /></label>
			<label><input type="checkbox" bind:checked={gW} disabled={readOnly} /></label>
			<label><input type="checkbox" bind:checked={gX} disabled={readOnly} /></label>
		</div>
		<div class="chmod-row">
			<span class="role">Остальные:</span>
			<label><input type="checkbox" bind:checked={oR} disabled={readOnly} /></label>
			<label><input type="checkbox" bind:checked={oW} disabled={readOnly} /></label>
			<label><input type="checkbox" bind:checked={oX} disabled={readOnly} /></label>
		</div>
	</div>

	{#if !readOnly}
		<div class="chmod-actions">
			<Button size="sm" variant="secondary" loading={savingChmod} onclick={applyChmod}>
				Применить права ({octalMode})
			</Button>
		</div>
	{/if}
</div>

<style>
	.section-box {
		padding: 0.75rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.section-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.chmod-table {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		font-size: 0.82rem;
	}
	.chmod-header, .chmod-row {
		display: grid;
		grid-template-columns: 90px 1fr 1fr 1fr;
		align-items: center;
		text-align: center;
	}
	.chmod-header {
		font-weight: 600;
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}
	.chmod-row .role {
		text-align: left;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.chmod-actions {
		display: flex;
		justify-content: flex-end;
		margin-top: 0.35rem;
	}
</style>
