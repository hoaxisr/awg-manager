<script lang="ts">
	import { Badge, type BadgeSize } from '$lib/components/ui';
	import { m } from '$lib/i18n';

	interface Props {
		defaultRoute?: boolean;
		size?: BadgeSize;
	}

	let { defaultRoute = false, size = 'sm' }: Props = $props();
</script>

{#if !defaultRoute}
	<Badge
		variant="error"
		{size}
		title={m.tunnels_default_route_badge_title()}
	>
		not default
	</Badge>
{/if}
