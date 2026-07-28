<script lang="ts">
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { DownloadOutbound, Settings } from '$lib/types';
	import { displayOutboundName, maskSensitiveInText } from '$lib/utils/downloadRouteLabel';

	interface Props {
		settings: Settings;
		saving: boolean;
		outbounds: DownloadOutbound[];
		loading: boolean;
		error: string;
		onRefresh: () => void;
		onSelectRoute: (routeTag: string, routeKind?: DownloadOutbound['kind']) => void;
	}

	let {
		settings = $bindable(),
		saving,
		outbounds,
		loading,
		error,
		onRefresh,
		onSelectRoute,
	}: Props = $props();

	// Only these kinds are meaningful download routes; anything else the backend
	// might report (raw/legacy interfaces) is hidden. Order drives the section
	// order in the dropdown, mirroring the NDMS-style grouping.
	const KIND_ORDER: Record<string, number> = {
		direct: 0,
		awg: 1,
		singbox: 2,
		subscription: 3,
	};
	const KIND_GROUP: Record<string, string | undefined> = {
		direct: undefined, // Direct sits on top, ungrouped.
		awg: 'AWG-туннели',
		singbox: 'Sing-box туннели',
		subscription: 'Sing-box подписки',
	};
	function isKnownKind(kind: string): boolean {
		return kind in KIND_ORDER;
	}

	function optionLabel(ob: DownloadOutbound): string {
		return `${displayOutboundName(ob)}${ob.available ? '' : ' (unavailable)'}`;
	}

	function routeKey(tag: string, kind?: DownloadOutbound['kind']): string {
		return JSON.stringify({ tag, kind: kind || '' });
	}

	function parseRouteKey(key: string): { tag: string; kind?: DownloadOutbound['kind'] } {
		try {
			const parsed = JSON.parse(key) as { tag?: string; kind?: string };
			const tag = parsed.tag?.trim() || 'direct';
			const kind = parsed.kind?.trim() as DownloadOutbound['kind'] | undefined;
			return { tag, kind: kind || undefined };
		} catch {
			return { tag: key.trim() || 'direct' };
		}
	}

	const selectedTag = $derived(settings.download?.routeTag?.trim() || 'direct');
	const selectedKind = $derived(
		settings.download?.routeKind?.trim() ||
		(selectedTag === 'direct' ? 'direct' : ''),
	);
	const selectedValue = $derived.by(() => {
		const exact = outbounds.find((ob) => ob.tag === selectedTag && (!selectedKind || ob.kind === selectedKind));
		if (exact) {
			return routeKey(exact.tag, exact.kind);
		}
		const tagOnly = outbounds.find((ob) => ob.tag === selectedTag);
		if (tagOnly) {
			return routeKey(tagOnly.tag, tagOnly.kind);
		}
		return routeKey(selectedTag, selectedKind as DownloadOutbound['kind']);
	});
	const visibleOutbounds = $derived(
		outbounds
			.filter((ob) => isKnownKind(ob.kind))
			.slice()
			.sort((a, b) => (KIND_ORDER[a.kind] ?? 99) - (KIND_ORDER[b.kind] ?? 99)),
	);
	const hasSelected = $derived(
		visibleOutbounds.some((ob) => routeKey(ob.tag, ob.kind) === selectedValue),
	);
	const options = $derived.by(() => {
		const built: DropdownOption<string>[] = visibleOutbounds.map((ob) => ({
			value: routeKey(ob.tag, ob.kind),
			label: optionLabel(ob),
			disabled: !ob.available,
			group: KIND_GROUP[ob.kind],
		}));
		if (!hasSelected && selectedValue) {
			const extra = selectedKind ? `${maskSensitiveInText(selectedTag)} (${selectedKind})` : maskSensitiveInText(selectedTag);
			built.unshift({
				value: selectedValue,
				label: `Недоступный маршрут: ${extra}`,
				disabled: true,
			});
		}
		return built;
	});

	function handleChange(v: string) {
		const selected = parseRouteKey(v);
		if (selected.tag === 'direct') {
			onSelectRoute('direct', 'direct');
			return;
		}
		onSelectRoute(selected.tag, selected.kind);
	}

	// Info-попап с пояснением, через что реально идут загрузки. Вынесен из
	// основного описания, чтобы не перегружать строку. Закрытие — клик вне
	// области и Escape.
	let infoOpen = $state(false);
	let infoHintEl = $state<HTMLElement | null>(null);

	function closeInfoOnOutside(e: MouseEvent) {
		if (!infoOpen) return;
		if (infoHintEl && !infoHintEl.contains(e.target as Node)) {
			infoOpen = false;
		}
	}

	function closeInfoOnEscape(e: KeyboardEvent) {
		if (e.key === 'Escape') infoOpen = false;
	}
</script>

<svelte:window onclick={closeInfoOnOutside} onkeydown={closeInfoOnEscape} />

<div id="downloads" class="setting-row download-setting">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Служебные загрузки AWGM</span>
		<div class="download-description" bind:this={infoHintEl}>
			<span class="setting-description">
				Маршрут для служебных задач: обновления AWGM и Sing-Box, загрузок geo.dat и DNSRoute-списков, а также конфигураций Amnezia Premium.<span
					class="info-hint"
				>
					<button
						type="button"
						class="info-trigger"
						aria-label="Через что идут загрузки"
						aria-expanded={infoOpen}
						onclick={() => (infoOpen = !infoOpen)}
					>
						<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
							<circle cx="8" cy="8" r="7" fill="none" stroke="currentColor" stroke-width="1.4" />
							<circle cx="8" cy="4.8" r="0.95" fill="currentColor" />
							<rect x="7.25" y="6.9" width="1.5" height="4.8" rx="0.75" fill="currentColor" />
						</svg>
					</button>
				</span>
			</span>
			{#if infoOpen}
				<div class="info-popup" role="tooltip">
					<span class="info-popup-title">Через какой маршрут идёт загрузка</span>
					<span class="info-popup-row">
						<strong>AWG-туннели</strong> — качают напрямую, отдельный sing-box не нужен.
					</span>
					<span class="info-popup-row">
						<strong>sing-box-туннели и подписки (SUB)</strong> — работают только при запущенном
						sing-box; если он выключен, маршрут будет недоступен.
					</span>
					<span class="info-popup-row">
						<strong>Загрузка самих подписок</strong> (скачивание их содержимого по URL) — всегда
						идёт напрямую через WAN, мимо туннеля.
					</span>
				</div>
			{/if}
		</div>
		{#if error}
			<span class="download-error">{error}</span>
		{/if}
	</div>
	<div class="download-controls">
		<div class="route-select">
			<Dropdown
				value={selectedValue}
				options={options}
				onchange={handleChange}
				disabled={saving || loading || options.length === 0}
				fullWidth
			/>
		</div>
		<div class="download-action">
			<Button
				variant="secondary"
				size="md"
				onclick={onRefresh}
				disabled={saving || loading}
			>
				Обновить список
			</Button>
		</div>
	</div>
</div>

<style>
	#downloads {
		scroll-margin-top: 5.5rem;
	}

	.download-setting {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, min(50%, 34rem));
		gap: 1rem;
		align-items: center;
	}

	.download-setting > :first-child {
		min-width: 0;
	}

	.download-controls {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: stretch;
		gap: 0.5rem;
		width: 100%;
		min-width: 0;
	}

	.route-select {
		width: 100%;
		min-width: 0;
		max-width: 100%;
	}

	.download-action {
		display: flex;
		align-items: stretch;
		white-space: nowrap;
	}

	.download-action :global(.btn) {
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		box-sizing: border-box;
		padding-block: 0;
	}

	.download-error {
		color: var(--color-error);
		font-size: 0.75rem;
	}

	.download-description {
		position: relative;
		min-width: 0;
	}

	.info-hint {
		position: relative;
		display: inline-flex;
		vertical-align: middle;
		margin-left: 0.25rem;
	}

	.info-trigger {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1.05rem;
		height: 1.05rem;
		padding: 0;
		border: none;
		border-radius: 50%;
		background: transparent;
		color: var(--text-muted, var(--color-text-muted));
		cursor: pointer;
		transition: color 0.12s ease;
	}

	.info-trigger:hover,
	.info-trigger[aria-expanded='true'] {
		color: var(--accent);
	}

	.info-trigger:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
		border-radius: 50%;
	}

	.info-popup {
		position: absolute;
		top: calc(100% + 0.4rem);
		right: 0;
		left: auto;
		z-index: 30;
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		width: max-content;
		max-width: min(22rem, calc(100vw - 1.5rem));
		padding: 0.6rem 0.75rem;
		border: 1px solid var(--border, var(--color-border));
		border-radius: var(--radius-sm);
		background: var(--bg-secondary, var(--color-bg-secondary));
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--text-primary, var(--color-text-primary));
		white-space: normal;
		text-align: left;
		cursor: default;
	}

	.info-popup-title {
		font-weight: 600;
		color: var(--text-primary, var(--color-text-primary));
	}

	.info-popup-row {
		color: var(--text-muted, var(--color-text-muted));
	}

	.info-popup-row strong {
		color: var(--text-primary, var(--color-text-primary));
		font-weight: 600;
	}

	@media (min-width: 641px) {
		.download-setting > :first-child {
			display: flex;
			flex-direction: column;
			align-items: flex-start;
			gap: 0.25rem;
		}

		.download-setting .setting-description {
			white-space: normal;
			overflow: visible;
			text-overflow: clip;
		}

		.download-controls {
			width: 100%;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: stretch;
		}

		.download-action :global(.btn) {
			width: auto;
			min-width: 7.5rem;
		}
	}

	@media (max-width: 640px) {
		.download-setting {
			grid-template-columns: 1fr;
		}

		.download-controls {
			grid-template-columns: minmax(0, 1fr) auto;
		}

		.info-popup {
			position: static;
			width: 100%;
			max-width: 100%;
			margin-top: 0.5rem;
		}
	}
</style>
