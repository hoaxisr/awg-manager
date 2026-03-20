<script lang="ts">
	import { getServiceIcon } from '$lib/utils/service-icons';

	interface Props {
		name: string;
		size?: number;
	}

	let { name, size = 36 }: Props = $props();

	let icon = $derived(getServiceIcon(name));
	let innerSize = $derived(Math.round(size * 0.56));
</script>

<div
	class="service-icon"
	style="width: {size}px; height: {size}px; background: {icon.background};"
>
	<svg
		viewBox={icon.viewBox ?? '0 0 24 24'}
		width={innerSize}
		height={innerSize}
	>
		{@html icon.svg}
	</svg>
</div>

<style>
	.service-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: 8px;
		flex-shrink: 0;
	}
</style>
