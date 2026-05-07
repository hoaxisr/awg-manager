<script lang="ts">
	interface Props {
		href: string;
	}

	let { href }: Props = $props();
</script>

<a {href} class="back-link">
	<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<line x1="19" y1="12" x2="5" y2="12"/>
		<polyline points="12 19 5 12 12 5"/>
	</svg>
	Назад
</a>
