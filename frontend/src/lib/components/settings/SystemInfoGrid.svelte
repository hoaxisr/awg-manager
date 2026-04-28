<script lang="ts">
	import type { SystemInfo } from '$lib/types';

	interface Props {
		systemInfo: SystemInfo;
	}

	let { systemInfo }: Props = $props();
</script>

<div class="card">
	<div class="section-label">Система</div>

	<div class="setting-row">
		<span class="info-key">AWGM</span>
		<span class="info-val">{systemInfo.version}</span>
	</div>
	<div class="setting-row">
		<span class="info-key">Роутер</span>
		<span class="info-val">{systemInfo.kernelModuleModel || '—'}</span>
	</div>
	<div class="setting-row">
		<span class="info-key">ОС</span>
		<span class="info-val">{systemInfo.firmwareVersion || systemInfo.keeneticOS || '—'}</span>
	</div>
	<div class="setting-row">
		<span class="info-key">Связь</span>
		<a class="info-link" href="https://t.me/awgmanager" target="_blank" rel="noopener noreferrer">Telegram →</a>
	</div>
</div>

<style>
	.info-key {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.info-val {
		font-size: 0.8125rem;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
	}

	.info-link {
		font-size: 0.8125rem;
		color: var(--color-accent);
		text-decoration: none;
	}

	.info-link:hover {
		text-decoration: underline;
	}
</style>
