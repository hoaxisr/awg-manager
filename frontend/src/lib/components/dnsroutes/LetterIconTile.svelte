<script lang="ts">
	import { letterIconBackground, letterIconGlyph } from '$lib/utils/letter-icon-color';

	interface Props {
		label: string;
		size?: number;
	}

	let { label, size = 36 }: Props = $props();

	let glyph = $derived(letterIconGlyph(label));
	let background = $derived(letterIconBackground(label));
	let fontSize = $derived(Math.max(11, Math.round(size * 0.46)));
</script>

<div
	class="letter-icon-tile"
	style:width="{size}px"
	style:height="{size}px"
	style:background
	style:font-size="{fontSize}px"
	aria-hidden="true"
>
	{glyph}
</div>

<style>
	.letter-icon-tile {
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: 6px;
		flex-shrink: 0;
		color: #fff;
		font-weight: 600;
		line-height: 1;
		user-select: none;
	}
</style>
