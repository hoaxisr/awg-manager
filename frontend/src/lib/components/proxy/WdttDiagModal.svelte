<script lang="ts">
	import { m } from '$lib/i18n';
	import {
		Button,
		Input,
		Modal,
		SegmentedControl,
	} from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { isAbortError } from '$lib/api/clientCore';
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
	import WdttDiagResultView from './WdttDiagResultView.svelte';

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
			if (token !== runToken || isAbortError(err)) return;
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
				<span class="diag-spinner" aria-hidden="true"></span>
				<span>{m.proxy_wdtt_diag_running()}</span>
			</div>
		{:else if !result && !errorText}
			<p class="idle-hint">{m.proxy_wdtt_diag_idle_hint()}</p>
		{/if}

		{#if errorText}
			<div class="diag-err-box" role="alert">
				<p class="diag-err-msg">{errorText}</p>
			</div>
		{:else if result}
			<WdttDiagResultView {result} />
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
		/* Сегменты делят ширину блока поровну: без минимума подпись сжимает их до «A…». */
		min-width: 12rem;
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

	.diag-spinner {
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

	.diag-err-box {
		border: 1px solid var(--color-error-border);
		border-radius: var(--radius);
		padding: 0.75rem 0.875rem;
		background: var(--color-error-tint);
	}

	.diag-err-msg {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.45;
	}
</style>
