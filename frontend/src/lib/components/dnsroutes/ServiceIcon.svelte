<script lang="ts">
	import { getServiceIcon, hasServiceIconKeywordMatch } from '$lib/utils/service-icons';
	import { resolveIconSlug, isPresetIconResolvable } from '$lib/utils/resolve-icon-slug';
	import { resolveIconTileBackground } from '$lib/utils/icon-tile-background';
	import { iconImageSrc } from '$lib/utils/icon-url-meta';
	import PresetIcon from '$lib/components/routing/singboxRouter/PresetIcon.svelte';
	import { serviceLetterIcons } from '$lib/stores/serviceLetterIcons';
	import IconTile from './IconTile.svelte';
	import LetterIconTile from './LetterIconTile.svelte';

	interface Props {
		name: string;
		size?: number;
		iconUrl?: string;
		/** sing-box preset slug; overrides name-based resolution */
		iconSlug?: string;
	}

	let { name, size = 36, iconUrl, iconSlug }: Props = $props();

	let imgFailed = $state(false);

	$effect(() => {
		void iconUrl;
		imgFailed = false;
	});

	// Fallback chain:
	//   1. explicit iconUrl (user-picked Qure / custom URL) → tiled <img>
	//   2. PresetIcon via iconSlug (preset id / exact name / brand slug)
	//   3. keyword inline SVG (service-icons.ts, substring match)
	//   4. letter monogram (NDMS / HR, only when nothing above matched)
	//   5. globe default (when service letter icons disabled in settings)
	let slug = $derived(resolveIconSlug(name, iconSlug));
	let usePreset = $derived(!iconUrl && !!slug && isPresetIconResolvable(slug));
	let hasKeywordIcon = $derived(hasServiceIconKeywordMatch(name));

	let renderSrc = $derived(iconUrl && !imgFailed ? iconImageSrc(iconUrl) : null);
	let tileBg = $derived(iconUrl ? resolveIconTileBackground(name, iconUrl) : '');

	let useLetter = $derived(
		$serviceLetterIcons && !iconUrl && !usePreset && !hasKeywordIcon,
	);

	let inlineIcon = $derived(getServiceIcon(name));
	let innerSize = $derived.by(() => {
		if (inlineIcon.assetSrc && inlineIcon.assetFit === 'cover') return size;
		return Math.round(size * (inlineIcon.scale ?? 0.56));
	});
</script>

{#if renderSrc}
	<IconTile
		src={renderSrc}
		background={tileBg}
		{size}
		alt={name}
		onerror={() => (imgFailed = true)}
	/>
{:else if usePreset && slug}
	<PresetIcon {slug} {size} label={name} />
{:else if useLetter}
	<LetterIconTile label={name} {size} />
{:else}
	<div
		class="service-icon"
		style="width: {size}px; height: {size}px; background: {inlineIcon.background};"
	>
		{#if inlineIcon.assetSrc}
			<img
				class="asset"
				class:cover={inlineIcon.assetFit === 'cover'}
				src={inlineIcon.assetSrc}
				alt={name}
				width={innerSize}
				height={innerSize}
				style:filter={inlineIcon.assetFilter ?? 'none'}
				loading="lazy"
			/>
		{:else}
			<svg
				viewBox={inlineIcon.viewBox ?? '0 0 24 24'}
				width={innerSize}
				height={innerSize}
			>
				{@html inlineIcon.svg ?? ''}
			</svg>
		{/if}
	</div>
{/if}

<style>
	.service-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: 6px;
		flex-shrink: 0;
	}
	.service-icon .asset {
		object-fit: contain;
	}
	.service-icon .asset.cover {
		width: 100%;
		height: 100%;
		object-fit: cover;
	}
</style>
