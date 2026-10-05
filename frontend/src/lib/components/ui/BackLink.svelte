<script lang="ts">
	import { m } from '$lib/i18n';
	import { ArrowLeft } from 'lucide-svelte';

	interface Props {
		href: string;
		label?: string;
	}

	let { href, label }: Props = $props();
</script>

<a {href} class="back-link">
	<ArrowLeft size={20} strokeWidth={2} aria-hidden="true" />
	{label ?? m.common_back()}
</a>
