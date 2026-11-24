<script lang="ts">
	import { m } from '$lib/i18n';
	import { Badge, type BadgeVariant } from '$lib/components/ui';
	import type { WdttDiagResult } from '$lib/types';

	interface Props {
		result: WdttDiagResult;
	}

	let { result }: Props = $props();

	function stepBadgeVariant(state: string): BadgeVariant {
		switch (state) {
			case 'ok':
				return 'success';
			case 'warn':
				return 'warning';
			case 'fail':
				return 'error';
			case 'run':
				return 'info';
			default:
				return 'muted';
		}
	}

	function stepBadgeLabel(state: string): string {
		switch (state) {
			case 'ok':
				return m.proxy_wdtt_diag_step_ok();
			case 'warn':
				return m.proxy_wdtt_diag_step_warn();
			case 'fail':
				return m.proxy_wdtt_diag_step_fail();
			case 'skip':
				return m.proxy_wdtt_diag_step_skip();
			case 'run':
				return m.proxy_wdtt_diag_step_run();
			default:
				return state;
		}
	}

	function verdictClass(sev: string): string {
		switch (sev) {
			case 'ok':
				return 'verdict--ok';
			case 'warn':
				return 'verdict--warn';
			default:
				return 'verdict--error';
		}
	}
</script>

{#if result.verdict}
	<div class={`verdict ${verdictClass(result.verdict.sev)}`}>
		<div class="verdict-head">
			<p class="verdict-title">{result.verdict.title}</p>
			<div class="verdict-meta">
				{#if result.mode}
					<Badge size="xs" variant="default" mono>{m.proxy_wdtt_diag_mode({ mode: result.mode })}</Badge>
				{/if}
				<Badge size="xs" variant="muted" mono>{m.proxy_wdtt_diag_duration({ ms: result.durationMs })}</Badge>
			</div>
		</div>
		<p class="verdict-text">{result.verdict.text}</p>
		{#if result.verdict.actions?.length}
			<div class="subblock">
				<p class="subblock-title">{m.proxy_wdtt_diag_actions()}</p>
				<ul class="actions-list">
					{#each result.verdict.actions as action, i (`${i}:${action}`)}
						<li>{action}</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#if result.verdict.facts && Object.keys(result.verdict.facts).length > 0}
			<div class="subblock">
				<p class="subblock-title">{m.proxy_wdtt_diag_facts()}</p>
				<div class="facts">
					{#each Object.entries(result.verdict.facts) as [k, v] (k)}
						<Badge size="xs" variant="default" mono>{k}={String(v)}</Badge>
					{/each}
				</div>
			</div>
		{/if}
	</div>
{/if}

{#if result.steps.length > 0}
	<div class="steps-section">
		<p class="section-title">{m.proxy_wdtt_diag_steps()}</p>
		<ul class="steps-list">
			{#each result.steps as step (step.step)}
				<li class="step-row">
					<Badge size="xs" variant={stepBadgeVariant(step.state)}>
						{stepBadgeLabel(step.state)}
					</Badge>
					<div class="step-body">
						<span class="step-title">{step.title || step.step}</span>
						{#if step.text}
							<span class="step-text">{step.text}</span>
						{/if}
					</div>
				</li>
			{/each}
		</ul>
	</div>
{/if}

<style>
	.verdict {
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 0.75rem 0.875rem;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.verdict--ok {
		border-color: var(--color-success-border);
		background: var(--color-success-tint);
	}

	.verdict--warn {
		border-color: var(--color-warning-border);
		background: var(--color-warning-tint);
	}

	.verdict--error {
		border-color: var(--color-error-border);
		background: var(--color-error-tint);
	}

	.verdict-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.verdict-title {
		margin: 0;
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.verdict--error .verdict-title {
		color: var(--color-error);
	}

	.verdict--warn .verdict-title {
		color: var(--color-warning);
	}

	.verdict--ok .verdict-title {
		color: var(--color-success);
	}

	.verdict-meta {
		display: inline-flex;
		gap: 0.375rem;
		align-items: center;
	}

	.verdict-text {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.45;
	}

	.subblock {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.subblock-title,
	.section-title {
		margin: 0;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.actions-list {
		margin: 0;
		padding-left: 1.125rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.facts {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.steps-section {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.steps-list {
		margin: 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.step-row {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		padding: 0.4375rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.step-body {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.step-title {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.step-text {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-muted);
		word-break: break-word;
	}
</style>
