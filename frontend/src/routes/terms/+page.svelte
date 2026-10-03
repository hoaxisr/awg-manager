<script lang="ts">
	import { m } from '$lib/i18n';
	import RichText from '$lib/components/ui/RichText.svelte';

</script>


<svelte:head>
	<title>{m.terms_page_title()}</title>
</svelte:head>

<div class="terms-page">
	<div class="terms-card">
		<h1>{m.terms_title()}</h1>
		<p class="intro"><RichText text={m.terms_intro()} /></p>

		<section>
			<h2>{m.terms_s1_title()}</h2>
			<p><RichText text={m.terms_s1_p1()} /></p>
			<p><RichText text={m.terms_s1_p2()} /></p>
		</section>

		<section>
			<h2>{m.terms_s2_title()}</h2>
			<p><RichText text={m.terms_s2_p1()} /></p>
			<ul>
				<li>{m.terms_s2_li1()}</li>
				<li>{m.terms_s2_li2()}</li>
				<li>{m.terms_s2_li3()}</li>
				<li>{m.terms_s2_li4()}</li>
			</ul>
			<p>{m.terms_s2_p2()}</p>
		</section>

		<section>
			<h2>{m.terms_s3_title()}</h2>
			<p>{m.terms_s3_p1()}</p>
			<ul>
				<li>{m.terms_s3_li1()}</li>
				<li>{m.terms_s3_li2()}</li>
				<li>{m.terms_s3_li3()}</li>
			</ul>
		</section>

		<section>
			<h2>{m.terms_s4_title()}</h2>
			<p>{m.terms_s4_p1()}</p>
			<p><RichText text={m.terms_s4_p2()} /></p>
			<p>{m.terms_s4_p3()}</p>
		</section>

		<section>
			<h2>{m.terms_s5_title()}</h2>
			<p><RichText text={m.terms_s5_p1()} /></p>
			<p>{m.terms_s5_p2()}</p>
			<p><RichText text={m.terms_s5_p3()} /></p>
			<p><RichText text={m.terms_s5_p4()} /></p>
		</section>

		<section>
			<h2>{m.terms_s6_title()}</h2>
			<p><RichText text={m.terms_s6_p1()} /></p>
			<p><RichText text={m.terms_s6_p2()} /></p>
			<p><RichText text={m.terms_s6_p3()} /></p>
			<p>{m.terms_s6_p4()}</p>
			<p><RichText text={m.terms_s6_p5()} /></p>
		</section>

		<section>
			<h2>{m.terms_s7_title()}</h2>
			<p class="no-support">{m.terms_s7_no_support()}</p>
			<p>{m.terms_s7_p1()}</p>
			<pre><code>opkg remove awg-manager</code></pre>
			<p><RichText text={m.terms_s7_p2()} /></p>
			<ol>
				<li>{m.terms_s7_li1()}</li>
				<li>{m.terms_s7_li2()}</li>
				<li>{m.terms_s7_li3()}</li>
				<li>{m.terms_s7_li4()}</li>
				<li>{m.terms_s7_li5()}</li>
				<li style="opacity: 0.5;">{m.terms_s7_li6()}</li>
			</ol>
			<p>{m.terms_s7_p3()}</p>

			<div class="faq">
				<div class="faq-item">
					<p class="faq-q">{m.terms_faq_q1()}</p>
					<p class="faq-a"><RichText text={m.terms_faq_a1({ link: '<a>aviasales.ru</a>' })} href="https://aviasales.ru" /></p>
				</div>
				<div class="faq-item">
					<p class="faq-q">{m.terms_faq_q2()}</p>
					<p class="faq-a">{m.terms_faq_a2()}</p>
				</div>
			</div>
		</section>

		<div class="acceptance-banner" role="note">
			<p><RichText text={m.terms_acceptance()} /></p>
		</div>

		<footer class="terms-footer">
			<a href="/" class="back-link">{m.terms_back()}</a>
		</footer>
	</div>
</div>

<style>
	.terms-page {
		min-height: 100vh;
		display: flex;
		justify-content: center;
		padding: 2rem 1rem;
		background: var(--color-bg-primary);
	}

	.terms-card {
		width: 100%;
		max-width: 680px;
		padding: 2rem 2.5rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		align-self: flex-start;
	}

	h1 {
		font-size: 1.5rem;
		font-weight: 700;
		margin: 0 0 0.75rem;
	}

	.intro {
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		margin-bottom: 2rem;
		line-height: 1.6;
	}

	section {
		margin-bottom: 1.75rem;
	}

	h2 {
		font-size: 1rem;
		font-weight: 600;
		margin: 0 0 0.625rem;
		color: var(--color-text);
	}

	p {
		font-size: 0.9rem;
		color: var(--color-text-secondary);
		line-height: 1.65;
		margin: 0 0 0.625rem;
	}

	.terms-card ul,
	.terms-card ol {
		margin: 0 0 0.625rem;
		padding-left: 1.75rem;
		list-style-position: outside;
	}

	.terms-card ul {
		list-style-type: disc;
	}

	.terms-card ol {
		list-style-type: decimal;
	}

	.terms-card li {
		font-size: 0.9rem;
		color: var(--color-text-secondary);
		line-height: 1.65;
		margin-bottom: 0.25rem;
		padding-left: 0.25rem;
	}

	.terms-card li::marker {
		color: var(--color-text-muted);
	}

	pre {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.625rem 0.875rem;
		margin: 0.5rem 0 0.75rem;
		overflow-x: auto;
	}

	code,
	.terms-card :global(code) {
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.85rem;
		color: var(--color-text);
	}

	.no-support {
		font-weight: 600;
		color: var(--color-text) !important;
	}

	.terms-card :global(.muted) {
		color: var(--color-text-muted);
		font-size: 0.85em;
	}

	.faq {
		margin-top: 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.faq-item {
		border-left: 2px solid var(--color-border);
		padding-left: 0.875rem;
	}

	.faq-q {
		font-style: italic;
		font-weight: 500;
		color: var(--color-text) !important;
		margin-bottom: 0.25rem !important;
	}

	.faq-a {
		margin: 0 !important;
	}

	.faq-a :global(a) {
		color: var(--color-accent);
		text-decoration: none;
	}

	.faq-a :global(a:hover) {
		text-decoration: underline;
	}

	.acceptance-banner {
		margin-top: 0.5rem;
		margin-bottom: 1.75rem;
		padding: 1rem 1.125rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-accent);
		border-radius: var(--radius-sm);
	}

	.acceptance-banner p {
		margin: 0;
		font-size: 0.9rem;
		color: var(--color-text);
		line-height: 1.65;
	}

	.acceptance-banner :global(code) {
		font-size: 0.85em;
	}

	.terms-footer {
		margin-top: 2rem;
		padding-top: 1.25rem;
		border-top: 1px solid var(--color-border);
	}

	.back-link {
		font-size: 0.875rem;
		color: var(--color-accent);
		text-decoration: none;
	}

	.back-link:hover {
		text-decoration: underline;
	}

	@media (max-width: 640px) {
		.terms-card {
			padding: 1.5rem 1.25rem;
		}
	}
</style>
