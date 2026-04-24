<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { DeviceProxyOutbound, DeviceProxyRuntime } from '$lib/types';

	interface Props {
		outbounds: DeviceProxyOutbound[];
		runtime: DeviceProxyRuntime;
		onSwitched: () => void;
	}

	let { outbounds, runtime, onSwitched }: Props = $props();

	let switching = $state(false);
	let applying = $state(false);

	async function handleSelect(tag: string) {
		if (switching || !runtime.alive) return;
		const current = runtime.activeTag || runtime.defaultTag;
		if (tag === current) return;

		switching = true;
		try {
			await api.selectDeviceProxyRuntime(tag);
			notifications.success(`Активный туннель: ${labelFor(tag)}`);
			onSwitched();
		} catch (e) {
			notifications.error(`Не удалось переключить: ${(e as Error).message}`);
		} finally {
			switching = false;
		}
	}

	async function applyNow() {
		if (applying) return;
		applying = true;
		try {
			await api.applyDeviceProxy();
			notifications.success('Перезапуск выполнен, новая конфигурация активна');
			onSwitched();
		} catch (e) {
			notifications.error(`Не удалось применить: ${(e as Error).message}`);
		} finally {
			applying = false;
		}
	}

	function labelFor(tag: string): string {
		const ob = outbounds.find((o) => o.tag === tag);
		return ob?.label ?? tag;
	}

	let currentTag = $derived(runtime.alive ? (runtime.activeTag || runtime.defaultTag) : runtime.defaultTag);
	let isTemporary = $derived(runtime.alive && runtime.activeTag !== '' && runtime.activeTag !== runtime.defaultTag);
	let defaultLabel = $derived(labelFor(runtime.defaultTag));

	let grouped = $derived.by(() => {
		const direct = outbounds.filter((o) => o.kind === 'direct');
		const sb = outbounds.filter((o) => o.kind === 'singbox');
		const awg = outbounds.filter((o) => o.kind === 'awg');
		return { direct, sb, awg };
	});
</script>

<section class="card">
	<div class="card-header">
		<h2 class="section-title">Активный туннель</h2>
		<span class={runtime.alive ? 'badge badge-success' : 'badge badge-muted'}>
			{runtime.alive ? 'Работает сейчас' : 'Применится при запуске'}
		</span>
	</div>
	<p class="section-desc">
		Переключение применяется моментально, без перезапуска sing-box. Действует до следующей перезагрузки прокси — тогда возьмётся значение "По умолчанию" из настроек.
	</p>

	<div class="select-row">
		<span class="row-label">Куда направляется трафик</span>
		<select
			class="select"
			disabled={switching || !runtime.alive}
			onchange={(e) => handleSelect((e.target as HTMLSelectElement).value)}
			value={currentTag}
		>
			{#each grouped.direct as ob (ob.tag)}
				<option value={ob.tag}>{ob.label}</option>
			{/each}
			{#if grouped.sb.length > 0}
				<optgroup label="Sing-box туннели">
					{#each grouped.sb as ob (ob.tag)}
						<option value={ob.tag}>{ob.label}</option>
					{/each}
				</optgroup>
			{/if}
			{#if grouped.awg.length > 0}
				<optgroup label="Туннели">
					{#each grouped.awg as ob (ob.tag)}
						<option value={ob.tag}>{ob.label} · {ob.detail}</option>
					{/each}
				</optgroup>
			{/if}
		</select>
	</div>

	{#if isTemporary}
		<div class="hint-row">
			<div class="hint-text">
				<span class="badge badge-warning">временно</span>
				После перезапуска вернётся к "{defaultLabel}"
			</div>
			<button
				type="button"
				class="btn btn-ghost btn-sm"
				disabled={applying}
				onclick={applyNow}
			>
				{applying ? 'Применяю…' : 'Применить сейчас'}
			</button>
		</div>
	{:else if !runtime.alive}
		<div class="hint-row">
			<div class="hint-text">Запустите sing-box, чтобы переключать вживую.</div>
		</div>
	{/if}
</section>

<style>
	.section-title { font-size: 1rem; font-weight: 600; margin: 0; }
	.section-desc { font-size: 0.8125rem; color: var(--text-muted); margin: 0 0 0.75rem 0; }

	.select-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		padding: 0.5rem 0;
	}
	.row-label {
		color: var(--text-primary);
		font-size: 0.875rem;
		font-weight: 500;
	}

	.select {
		min-width: 260px;
		max-width: 60%;
		padding: 0.4rem 0.6rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text-primary);
		font-size: 0.8125rem;
	}
	.select:disabled { opacity: 0.5; cursor: not-allowed; }

	.hint-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-top: 0.5rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-tertiary);
		border-radius: 6px;
	}
	.hint-text {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.badge-muted {
		background: rgba(107, 114, 128, 0.15);
		color: var(--text-muted);
	}
</style>
