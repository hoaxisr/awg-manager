<script lang="ts">
	import { Toggle } from '$lib/components/ui';
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
			<span class="restart-dot" class:rec={recovering} class:off={!on}></span>
			<span class="restart-name">{t.name}</span>
			{#if recovering}
				<span class="restart-rec">восстановление (попытка {pc?.restartCount ?? 0})</span>
			{:else if on}
				<span class="restart-threshold">порог: {pc?.failThreshold ?? 0}</span>
			{/if}
			<span class="restart-spacer"></span>
			<button
				type="button"
				class="route-action-btn"
				title={`Настроить переподнятие «${t.name}»`}
				onclick={() => onConfigure(t.id)}
			>
				<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.38a2 2 0 0 0-.73-2.73l-.15-.09a2 2 0 0 1-1-1.74v-.51a2 2 0 0 1 1-1.72l.15-.1a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
					<circle cx="12" cy="12" r="3" />
				</svg>
			</button>
			<Toggle checked={on} onchange={(next) => onToggleTunnel(t, next)} />
		</div>
	{/each}
</div>
