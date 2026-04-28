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
	<!-- Global Swagger UI theme overrides — scoped to this page via .swagger-root -->
	<style>
		/* ── Transparent scheme container in both themes ── */
		.swagger-ui .scheme-container {
			background: transparent !important;
			box-shadow: none !important;
			padding: 12px 0 !important;
		}

		/* ── Dark theme overrides ── */
		[data-theme="dark"] .swagger-ui,
		[data-theme="dark"] .swagger-ui .wrapper,
		[data-theme="dark"] .swagger-ui .opblock-tag-section {
			background: transparent;
			color: #c0caf5;
		}

		[data-theme="dark"] .swagger-ui .info .title,
		[data-theme="dark"] .swagger-ui .info h1,
		[data-theme="dark"] .swagger-ui .info h2,
		[data-theme="dark"] .swagger-ui .info p,
		[data-theme="dark"] .swagger-ui .info li,
		[data-theme="dark"] .swagger-ui .info a {
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui .info .base-url {
			color: #a9b1d6 !important;
		}

		/* Tags / operation headers */
		[data-theme="dark"] .swagger-ui .opblock-tag {
			border-bottom: 1px solid #3b4261 !important;
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui .opblock-tag:hover,
		[data-theme="dark"] .swagger-ui .opblock-tag small {
			color: #a9b1d6 !important;
		}

		/* Operation blocks */
		[data-theme="dark"] .swagger-ui .opblock {
			background: #1e2030 !important;
			border: 1px solid #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui .opblock .opblock-summary {
			border-bottom: 1px solid #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui .opblock .opblock-summary-description,
		[data-theme="dark"] .swagger-ui .opblock .opblock-summary-path,
		[data-theme="dark"] .swagger-ui .opblock .opblock-summary-path__deprecated {
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui .opblock-body {
			background: #1a1b26 !important;
		}

		/* Parameters / responses section */
		[data-theme="dark"] .swagger-ui .opblock-section-header {
			background: #24283b !important;
			border-bottom: 1px solid #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui .opblock-section-header h4,
		[data-theme="dark"] .swagger-ui .opblock-section-header label {
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui table thead tr th,
		[data-theme="dark"] .swagger-ui .parameters-col_description,
		[data-theme="dark"] .swagger-ui .parameters-col_name,
		[data-theme="dark"] .swagger-ui .col_header {
			color: #a9b1d6 !important;
			border-bottom: 1px solid #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui .parameter__name,
		[data-theme="dark"] .swagger-ui .parameter__type,
		[data-theme="dark"] .swagger-ui .parameter__in {
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui .renderedMarkdown p,
		[data-theme="dark"] .swagger-ui .renderedMarkdown li,
		[data-theme="dark"] .swagger-ui .markdown p,
		[data-theme="dark"] .swagger-ui .markdown li {
			color: #a9b1d6 !important;
		}

		/* Response codes & text */
		[data-theme="dark"] .swagger-ui .response-col_status {
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui .response-col_description {
			color: #a9b1d6 !important;
		}

		[data-theme="dark"] .swagger-ui .response-col_links {
			color: #a9b1d6 !important;
		}

		/* Code / models */
		[data-theme="dark"] .swagger-ui .highlight-code,
		[data-theme="dark"] .swagger-ui pre.microlight {
			background: #24283b !important;
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui .model-box,
		[data-theme="dark"] .swagger-ui section.models .model-container {
			background: #1e2030 !important;
			border: 1px solid #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui section.models {
			border: 1px solid #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui section.models h4,
		[data-theme="dark"] .swagger-ui .model-title,
		[data-theme="dark"] .swagger-ui .model .property,
		[data-theme="dark"] .swagger-ui .model .property.primitive,
		[data-theme="dark"] .swagger-ui .model span {
			color: #c0caf5 !important;
		}

		[data-theme="dark"] .swagger-ui .model-toggle::after {
			filter: invert(0.8) !important;
		}

		/* Form inputs / selects */
		[data-theme="dark"] .swagger-ui input[type="text"],
		[data-theme="dark"] .swagger-ui textarea,
		[data-theme="dark"] .swagger-ui select {
			background: #24283b !important;
			color: #c0caf5 !important;
			border: 1px solid #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui label {
			color: #a9b1d6 !important;
		}

		/* Try-it-out / execute */
		[data-theme="dark"] .swagger-ui .btn {
			color: #c0caf5 !important;
			border-color: #3b4261 !important;
		}

		[data-theme="dark"] .swagger-ui .btn.execute {
			background: #7aa2f7 !important;
			color: #1a1b26 !important;
			border-color: #7aa2f7 !important;
		}

		[data-theme="dark"] .swagger-ui .btn.cancel {
			color: #f7768e !important;
			border-color: #f7768e !important;
		}

		/* Topbar — hide it, nav is in the app shell */
		[data-theme="dark"] .swagger-ui .topbar,
		.swagger-ui .topbar {
			display: none !important;
		}

		/* Servers dropdown */
		[data-theme="dark"] .swagger-ui .servers-title,
		[data-theme="dark"] .swagger-ui .servers > label {
			color: #a9b1d6 !important;
		}

		[data-theme="dark"] .swagger-ui .servers select {
			background: #24283b !important;
			color: #c0caf5 !important;
			border: 1px solid #3b4261 !important;
		}

		/* Auth button */
		[data-theme="dark"] .swagger-ui .authorization__btn {
			color: #7aa2f7 !important;
		}

		/* Required asterisk */
		[data-theme="dark"] .swagger-ui .required {
			color: #f7768e !important;
		}
	</style>
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
		color: var(--color-text-secondary, #a9b1d6);
	}
	.hint code {
		font-size: 0.8125rem;
	}
	:global(.swagger-root) {
		min-height: 70vh;
	}
</style>
