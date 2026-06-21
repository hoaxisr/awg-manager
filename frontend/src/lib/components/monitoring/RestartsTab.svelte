<script lang="ts">
	import { Toggle, Button } from '$lib/components/ui';
	import type { TunnelListItem } from '$lib/types';

	interface Props {
		tunnels: TunnelListItem[];
		restartEnabled: boolean;
		onToggleMaster: (next: boolean) => void;
		onToggleTunnel: (t: TunnelListItem, next: boolean) => void;
		onConfigure: (tunnelId: string) => void;
	}
	let { tunnels, restartEnabled, onToggleMaster, onToggleTunnel, onConfigure }: Props = $props();
</script>

<div class="master-row">
	<span class="master-label">Авто-переподнятие (ping-check)</span>
	<Toggle checked={restartEnabled} onchange={onToggleMaster} />
</div>

<div class="restart-banner">
	Переподнятие перезапускает туннель (link-toggle) при нескольких неудачных проверках подряд.
</div>

<div class="restart-list">
	{#each tunnels as t (t.id)}
		{@const pc = t.pingCheck}
		{@const on = pc?.status !== 'disabled'}
		{@const recovering = pc?.status === 'recovering'}
		<div class="restart-row">
			<span class="restart-dot" class:rec={recovering}></span>
			<span class="restart-name">{t.name}</span>
			{#if recovering}
				<span class="restart-rec">восстановление (попытка {pc?.restartCount ?? 0})</span>
			{:else if on}
				<span class="restart-threshold">порог: {pc?.failThreshold ?? 0}</span>
			{/if}
			<span class="restart-spacer"></span>
			<Button variant="ghost" onclick={() => onConfigure(t.id)}>Настроить…</Button>
			<Toggle checked={on} onchange={(next) => onToggleTunnel(t, next)} />
		</div>
	{/each}
</div>
