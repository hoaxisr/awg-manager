<script lang="ts">
	import { onMount } from 'svelte';

	let root: HTMLDivElement | undefined;
	const specCandidates = ['/api/openapi.yaml', '/openapi.yaml'];

	onMount(() => {
		let destroyed = false;
		(async () => {
			await import('swagger-ui-dist/swagger-ui.css');
			const mod = await import('swagger-ui-dist/swagger-ui-bundle.js');
			const SwaggerUIBundle = (mod as { default?: unknown }).default ?? mod;
			if (destroyed || !root) return;

			let chosenURL = specCandidates[0];
			for (const candidate of specCandidates) {
				try {
					const res = await fetch(candidate, { method: 'GET' });
					if (res.ok) {
						chosenURL = candidate;
						break;
					}
				} catch {
					// try next candidate
				}
			}

			(SwaggerUIBundle as (opts: Record<string, unknown>) => { preauthorizeBasic?: unknown })(
				{
					domNode: root,
					url: chosenURL
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
		OpenAPI spec: first <code>/api/openapi.yaml</code>, fallback <code>/openapi.yaml</code> (file:
		<code>internal/openapi/swagger.yaml</code>, for fallback sync to <code>frontend/static/openapi.yaml</code>) —
		regenerate from repo root with
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
