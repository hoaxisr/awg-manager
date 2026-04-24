<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { DeviceProxyOutbound } from '$lib/types';

	interface Props {
		outbounds: DeviceProxyOutbound[];
		activeTag: string;
		singboxRunning: boolean;
		onChanged: (tag: string) => void;
	}

	let { outbounds, activeTag, singboxRunning, onChanged }: Props = $props();

	let switching = $state(false);

	async function handleSelect(tag: string) {
		if (tag === activeTag || switching) return;
		switching = true;
		try {
			await api.selectDeviceProxyOutbound(tag);
			onChanged(tag);
			notifications.success(`Активный outbound: ${tag}`);
		} catch (e) {
			notifications.error(`Не удалось переключить: ${(e as Error).message}`);
		} finally {
			switching = false;
		}
	}

	let grouped = $derived.by(() => {
		const direct = outbounds.filter((o) => o.kind === 'direct');
		const sb = outbounds.filter((o) => o.kind === 'singbox');
		const awg = outbounds.filter((o) => o.kind === 'awg');
		return { direct, sb, awg };
	});
</script>

<section class="card">
	<div class="card-header">
		<h2 class="section-title">Активный outbound</h2>
		<span class={singboxRunning ? 'badge badge-success' : 'badge badge-warning'}>
			{singboxRunning ? 'Live' : 'Применится при запуске'}
		</span>
	</div>

	<label class="sr-only" for="dp-outbound-select">Outbound</label>
	<select
		id="dp-outbound-select"
		disabled={switching}
		onchange={(e) => handleSelect((e.target as HTMLSelectElement).value)}
		value={activeTag}
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
			<optgroup label="AWG туннели">
				{#each grouped.awg as ob (ob.tag)}
					<option value={ob.tag}>{ob.label} · {ob.detail}</option>
				{/each}
			</optgroup>
		{/if}
	</select>
</section>

<style>
	.section-title {
		font-size: 1rem;
		font-weight: 600;
		margin: 0;
	}

	.sr-only {
		position: absolute;
		left: -9999px;
	}
</style>
