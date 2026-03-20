<script lang="ts">
	import type { SystemInfo } from '$lib/types';

	interface Props {
		systemInfo: SystemInfo;
	}

	let { systemInfo }: Props = $props();


</script>

<div class="info-grid">
	<div class="flex flex-col gap-1">
		<span class="grid-label">Версия</span>
		<span class="grid-value">{systemInfo.version}</span>
	</div>
	<div class="flex flex-col gap-1">
		<span class="grid-label">Модель роутера</span>
		<span class="grid-value">{systemInfo.kernelModuleModel || '—'}</span>
	</div>
	<div class="flex flex-col gap-1">
		<span class="grid-label">Версия ОС</span>
		<span class="grid-value">{systemInfo.firmwareVersion || systemInfo.keeneticOS || '—'}</span>
	</div>
	<div class="flex flex-col gap-1">
		<span class="grid-label">Канал общения</span>
		<a class="grid-value tg-link" href="https://t.me/awgmanager" target="_blank" rel="noopener">Telegram</a>
	</div>
</div>

<style>
	.info-grid {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 1rem;
	}

	.grid-label {
		font-size: 0.6875rem;
		text-transform: uppercase;
		color: var(--text-muted);
	}

	.grid-value {
		font-size: 0.9375rem;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
	}

	.tg-link {
		color: var(--accent);
		text-decoration: none;
	}

	.tg-link:hover {
		text-decoration: underline;
	}
</style>
