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
			{runtime.alive ? '● Работает сейчас' : '○ Применится при запуске'}
		</span>
	</div>
	<p class="section-desc">
		Переключение применяется моментально, без перезапуска sing-box. Действует до следующей перезагрузки прокси — тогда возьмётся значение "По умолчанию" из настроек.
	</p>

	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Куда направляется трафик</span>
			{#if isTemporary}
				<span class="setting-description">
					<span class="badge badge-warning">временно</span>
					После перезапуска вернётся к "{defaultLabel}"
				</span>
			{:else if !runtime.alive}
				<span class="setting-description">
					Запустите sing-box, чтобы переключать вживую
				</span>
			{/if}
		</div>
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
</section>

<style>
	.section-title { font-size: 1rem; font-weight: 600; margin: 0; }
	.section-desc { font-size: 0.8125rem; color: var(--text-muted); margin: 0 0 0.75rem 0; }
	.select {
		min-width: 240px;
		padding: 0.4rem 0.6rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text-primary);
		font-size: 0.8125rem;
	}
	.select:disabled { opacity: 0.5; cursor: not-allowed; }
	.badge-muted {
		background: rgba(107, 114, 128, 0.15);
		color: var(--text-muted);
	}
</style>
