<script lang="ts">
	import { m } from '$lib/i18n';
	import {
		Badge,
		Button,
		Input,
		Modal,
		SegmentedControl,
		type BadgeVariant,
	} from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import {
		WDTT_DIAG_BUSY,
		WDTT_DIAG_CONFIG_INCOMPLETE,
		WDTT_DIAG_NO_BINARY,
		WDTT_DIAG_TIMEOUT,
		WDTT_DIAG_UNSUPPORTED,
	} from '$lib/api/clientWdtt';
	import { errText } from '$lib/utils/errorMessage';
	import type { WdttDiagRequest, WdttDiagResult, WdttDiagTransport } from '$lib/types';
	import { apiErrorCode } from './serverClients';

	interface Props {
		open: boolean;
		instanceId: string;
		instanceName: string;
		diagKeySet?: boolean;
		onclose: () => void;
	}

	let {
		open,
		instanceId,
		instanceName,
		diagKeySet = false,
		onclose,
	}: Props = $props();

	let transport = $state<WdttDiagTransport>('auto');
	let diagKeyInput = $state('');
	let running = $state(false);
	let result = $state<WdttDiagResult | null>(null);
	let errorText = $state('');

	// Нереактивный внутренний учёт текущего HTTP-запроса: отсекает устаревшие
	// ответы и отменяет висящий fetch через AbortController, не вызывая $effect.
	let runToken = 0;
	let activeController: AbortController | null = null;

	const transportOptions = $derived([
		{ value: 'auto' as const, label: m.proxy_wdtt_diag_transport_auto() },
		{ value: 'udp' as const, label: 'UDP' },
		{ value: 'tcp' as const, label: 'TCP' },
	]);

	function mapDiagError(err: unknown): string {
		switch (apiErrorCode(err)) {
			case WDTT_DIAG_UNSUPPORTED:
				return m.proxy_wdtt_diag_err_unsupported();
			case WDTT_DIAG_BUSY:
				return m.proxy_wdtt_diag_err_busy();
			case WDTT_DIAG_TIMEOUT:
				return m.proxy_wdtt_diag_err_timeout();
			case WDTT_DIAG_NO_BINARY:
				return m.proxy_wdtt_diag_err_no_binary();
			case WDTT_DIAG_CONFIG_INCOMPLETE:
				return m.proxy_wdtt_diag_err_incomplete();
			default:
				return errText(err);
		}
	}

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

	function cancelDiag(): void {
		runToken += 1;
		if (activeController) {
			activeController.abort();
			activeController = null;
		}
		running = false;
	}

	async function runDiag(): Promise<void> {
		cancelDiag();
		const token = ++runToken;
		const controller = new AbortController();
		activeController = controller;
		running = true;
		errorText = '';
		try {
			const req: WdttDiagRequest = {};
			if (transport !== 'auto') {
				req.transport = transport;
			}
			const dk = diagKeyInput.trim();
			if (dk) {
				req.diagKey = dk;
			}
			const res = await api.runWdttClientDiag(instanceId, req, controller.signal);
			if (token !== runToken) return;
			result = res;
		} catch (err) {
			if (token !== runToken || (err instanceof Error && err.name === 'AbortError')) return;
			result = null;
			errorText = mapDiagError(err);
		} finally {
			if (activeController === controller) {
				activeController = null;
			}
			if (token === runToken) {
				running = false;
			}
		}
	}

	$effect(() => {
		if (!open) {
			cancelDiag();
		}
		return () => cancelDiag();
	});
</script>

<Modal
	{open}
	size="lg"
	title={m.proxy_wdtt_diag_title({ name: instanceName })}
	{onclose}
	bodyMinHeight="14rem"
>
	<div class="diag">
		<div class="controls">
			<div class="control-block">
				<span class="control-label">{m.proxy_wdtt_diag_transport()}</span>
				<SegmentedControl
					value={transport}
					options={transportOptions}
					ariaLabel={m.proxy_wdtt_diag_transport()}
					disabled={running}
					onchange={(v) => (transport = v)}
				/>
			</div>
			<div class="control-block control-block--grow">
				<Input
					id="wdtt-diag-key"
					label={m.proxy_wdtt_diag_diag_key()}
					bind:value={diagKeyInput}
					placeholder={m.proxy_wdtt_diag_diag_key_placeholder()}
					hint={diagKeySet ? m.proxy_wdtt_diag_diag_key_saved() : undefined}
					disabled={running}
					fullWidth
				/>
			</div>
		</div>

		{#if running}
			<div class="running-box" role="status">
				<span class="spinner" aria-hidden="true"></span>
				<span>{m.proxy_wdtt_diag_running()}</span>
			</div>
		{:else if !result && !errorText}
			<p class="idle-hint">{m.proxy_wdtt_diag_idle_hint()}</p>
		{/if}

		{#if errorText}
			<div class="verdict verdict--error" role="alert">
				<p class="verdict-text">{errorText}</p>
			</div>
		{:else if result}
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
		{/if}
	</div>

	{#snippet actions()}
		<Button
			variant={result || errorText ? 'secondary' : 'primary'}
			size="sm"
			loading={running}
			onclick={() => runDiag()}
		>
			{result || errorText ? m.proxy_wdtt_diag_retry() : m.proxy_wdtt_diag_run()}
		</Button>
		<Button variant="ghost" size="sm" onclick={onclose}>
			{m.proxy_wdtt_diag_close()}
		</Button>
	{/snippet}
</Modal>

<style>
	.diag {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.controls {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		align-items: flex-start;
	}

	.control-block {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.control-block--grow {
		flex: 1;
		min-width: 14rem;
	}

	.control-label {
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.running-box {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.625rem 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.idle-hint {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.45;
	}

	.spinner {
		width: 0.875rem;
		height: 0.875rem;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: wdtt-diag-spin 0.8s linear infinite;
		flex-shrink: 0;
	}

	@keyframes wdtt-diag-spin {
		to {
			transform: rotate(360deg);
		}
	}

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
