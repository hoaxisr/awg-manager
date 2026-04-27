<script lang="ts">
	import { onMount } from 'svelte';

	let root: HTMLDivElement | undefined;

	onMount(() => {
		let destroyed = false;
		(async () => {
			await import('swagger-ui-dist/swagger-ui.css');
			const mod = await import('swagger-ui-dist/swagger-ui-bundle.js');
			const SwaggerUIBundle = (mod as { default?: unknown }).default ?? mod;
			if (destroyed || !root) return;
			(SwaggerUIBundle as (opts: Record<string, unknown>) => { preauthorizeBasic?: unknown })(
				{
					domNode: root,
					url: '/api/openapi.yaml'
				}
			);
		})();
		return () => {
			destroyed = true;
		};
	});
</script>

<svelte:head>
	<title>API docs (dev)</title>
</svelte:head>

<div class="wrap">
	<p class="hint">
		OpenAPI spec: <a href="/api/openapi.yaml">/api/openapi.yaml</a> (file:
		<code>internal/openapi/swagger.yaml</code>) — regenerate from repo root with
		<code>go generate ./cmd/awg-manager</code>
	</p>
	<div bind:this={root} class="swagger-root"></div>
</div>

<style>
	.wrap {
		padding: 0.75rem 1rem;
		min-height: 100vh;
	}
	.hint {
		margin: 0 0 0.75rem;
		font-size: 0.875rem;
		color: var(--tw-prose-body, #444);
	}
	.hint code {
		font-size: 0.8125rem;
	}
	.swagger-root {
		min-height: 70vh;
	}
</style>
