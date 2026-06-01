<script lang="ts">
	import { QURE_ICON_INNER_SCALE } from '$lib/utils/icon-tile-background';

	interface Props {
		src: string;
		background: string;
		size?: number;
		innerScale?: number;
		alt?: string;
		onerror?: () => void;
	}

	let {
		src,
		background,
		size = 36,
		innerScale = QURE_ICON_INNER_SCALE,
		alt = '',
		onerror,
	}: Props = $props();

	let innerSize = $derived(Math.round(size * innerScale));
</script>

<div
	class="icon-tile"
	style:width="{size}px"
	style:height="{size}px"
	style:background={background}
>
	<img
		{src}
		{alt}
		width={innerSize}
		height={innerSize}
		loading="lazy"
		{onerror}
	/>
</div>

<style>
	.icon-tile {
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: 6px;
		flex-shrink: 0;
		overflow: hidden;
	}
	.icon-tile img {
		object-fit: contain;
	}
</style>
