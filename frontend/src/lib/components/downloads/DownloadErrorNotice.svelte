<script lang="ts">
	import {
		DOWNLOAD_SETTINGS_HREF,
		humanizeDownloadError,
		type HumanizedDownloadError,
	} from '$lib/utils/downloadError';

	interface Props {
		/** Anything caught at a call site: Error, API error, or string. */
		error: unknown;
		/** Hide the raw "details" disclosure (e.g. inside tight status lines). */
		hideRaw?: boolean;
		/** Suppress the "go to download settings" link — set when this notice
		 *  is already rendered inside the Settings → Downloads context. */
		hideSettingsLink?: boolean;
	}

	let { error, hideRaw = false, hideSettingsLink = false }: Props = $props();

	const info: HumanizedDownloadError = $derived(humanizeDownloadError(error));
	const showRaw = $derived(!hideRaw && info.kind !== 'generic' && info.raw.trim().length > 0);
	const showSettingsLink = $derived(info.needsDownloadSettings && !hideSettingsLink);
</script>

<div class="dl-error" class:dl-error-singbox={info.kind === 'singbox-off' || info.kind === 'singbox-route'}>
	<span class="dl-error-title">{info.title}</span>
	{#if info.detail}
		<span class="dl-error-detail">{info.detail}</span>
	{/if}
	{#if showSettingsLink}
		<a class="dl-error-link" href={DOWNLOAD_SETTINGS_HREF}>
			Открыть Настройки → Загрузки
		</a>
	{/if}
	{#if showRaw}
		<details class="dl-error-raw">
			<summary>Подробности</summary>
			<code>{info.raw}</code>
		</details>
	{/if}
</div>

<style>
	.dl-error {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		font-size: 0.8125rem;
		line-height: 1.4;
		color: var(--error, var(--color-danger));
		min-width: 0;
	}

	.dl-error-title {
		font-weight: 600;
	}

	.dl-error-detail {
		color: var(--text-muted, var(--color-text-muted));
		font-weight: 400;
	}

	.dl-error-link {
		align-self: flex-start;
		color: var(--accent);
		text-decoration: underline;
		font-weight: 500;
	}

	.dl-error-raw {
		margin-top: 0.1rem;
		color: var(--text-muted, var(--color-text-muted));
	}

	.dl-error-raw summary {
		cursor: pointer;
		font-size: 0.75rem;
		color: var(--text-muted, var(--color-text-muted));
	}

	.dl-error-raw code {
		display: block;
		margin-top: 0.25rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm, 6px);
		background: color-mix(in srgb, var(--bg-secondary, #000) 60%, transparent);
		font-size: 0.7rem;
		white-space: pre-wrap;
		word-break: break-word;
		color: var(--text-primary, var(--color-text-primary));
	}
</style>
