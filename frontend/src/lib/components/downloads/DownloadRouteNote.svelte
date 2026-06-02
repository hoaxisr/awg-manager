<script lang="ts">
	import { settings, usageLevel } from '$lib/stores/settings';
	import {
		downloadOutbounds,
		downloadOutboundsError,
		downloadOutboundsLoaded,
		downloadOutboundsStatus,
		ensureDownloadOutboundsLoaded,
		resolveDownloadRouteLabel,
	} from '$lib/stores/downloadRoute';
	import { areDownloadRouteDetailsVisible } from '$lib/types/usageLevel';

	interface Props {
		text: string;
	}

	let { text }: Props = $props();
	const showDownloadRouteDetails = $derived(areDownloadRouteDetailsVisible($usageLevel));

	$effect(() => {
		if (showDownloadRouteDetails) {
			void ensureDownloadOutboundsLoaded();
		}
	});

	const routeLabel = $derived(resolveDownloadRouteLabel($settings, $downloadOutbounds));
	const isInitialLoading = $derived(
		!$downloadOutboundsLoaded &&
			($downloadOutboundsStatus === 'idle' || $downloadOutboundsStatus === 'loading'),
	);
	const isHardError = $derived($downloadOutboundsStatus === 'error');
	const isStale = $derived($downloadOutboundsStatus === 'stale');
	const noteText = $derived.by(() => {
		if (isInitialLoading) return 'Маршрут загрузки определяется…';
		if (isHardError) return 'Не удалось определить маршрут загрузки';
		return `${text} ${routeLabel}`;
	});
	const noteTitle = $derived.by(() => {
		if (isInitialLoading) return 'Загрузка списка маршрутов…';
		if (isHardError) return `Не удалось загрузить список маршрутов: ${$downloadOutboundsError}`;
		if (isStale) {
			return `${routeLabel}. Список маршрутов может быть устаревшим: ${$downloadOutboundsError}`;
		}
		return routeLabel;
	});
</script>

{#if showDownloadRouteDetails}
	<div
		class="download-route-note"
		class:download-route-note-loading={isInitialLoading}
		class:download-route-note-warn={isStale}
		class:download-route-note-error={isHardError}
		title={noteTitle}
	>
		{noteText}
	</div>
{/if}

<style>
	.download-route-note {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}
	.download-route-note-loading {
		color: var(--color-text-muted);
	}
	.download-route-note-warn {
		color: var(--warning, #f59e0b);
	}
	.download-route-note-error {
		color: var(--color-danger, #ef4444);
	}
</style>
