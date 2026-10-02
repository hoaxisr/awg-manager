<script lang="ts">
	import { TriangleAlert, CircleX } from 'lucide-svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { tunnelNameError } from '$lib/utils/tunnelName';
	import { m } from '$lib/i18n';

	interface Props {
		interfaceName: string;
		open?: boolean;
		error?: string;
		loading?: boolean;
		onclose?: () => void;
		onadopt?: (data: { content: string; name: string }) => void;
	}

	let {
		interfaceName,
		open = $bindable(false),
		error = $bindable(''),
		loading = $bindable(false),
		onclose,
		onadopt
	}: Props = $props();

	let step = $state<'upload' | 'instructions' | 'error'>('upload');
	let configContent = $state('');
	let tunnelName = $state('');
	let localError = $state('');

	$effect(() => {
		if (error) {
			step = 'error';
		}
	});

	function handleFileSelect(event: Event): void {
		const input = event.target as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;

		const reader = new FileReader();
		reader.onload = (e: ProgressEvent<FileReader>) => {
			configContent = e.target?.result as string;
		};
		reader.onerror = () => {
			localError = m.tunnels_read_file_failed();
		};
		reader.readAsText(file);
	}

	function handleNext(): void {
		if (!configContent.trim()) {
			localError = m.tunnels_adopt_upload_required();
			return;
		}
		localError = tunnelNameError(tunnelName);
		if (localError) return;
		step = 'instructions';
	}

	function handleConfirm(): void {
		onadopt?.({ content: configContent, name: tunnelName });
	}

	function handleClose(): void {
		step = 'upload';
		configContent = '';
		tunnelName = '';
		localError = '';
		error = '';
		onclose?.();
	}

	function displayError(): string {
		return error || localError;
	}
</script>

<Modal
	{open}
	title={m.tunnels_adopt_title({ name: interfaceName })}
	onclose={handleClose}
	size="md"
>
	{#if step === 'upload'}
		<p class="dialog-description">
			{m.tunnels_adopt_description()}
		</p>

		<div class="form-group">
			<label for="config-file">{m.tunnels_adopt_config_file()}</label>
			<input
				type="file"
				id="config-file"
				accept=".conf,text/plain,application/octet-stream"
				onchange={handleFileSelect}
			/>
		</div>

		{#if configContent}
			<div class="form-group">
				<label for="tunnel-name">{m.tunnels_adopt_name_optional()}</label>
				<input
					type="text"
					id="tunnel-name"
					bind:value={tunnelName}
					placeholder="Imported tunnel"
				/>
			</div>
		{/if}

		{#if localError}
			<p class="error-text">{localError}</p>
		{/if}

	{:else if step === 'instructions'}
		<div class="alert alert-warning">
			<TriangleAlert size={24} aria-hidden="true" />
			<div>
				<h3>{m.tunnels_adopt_important()}</h3>
				<p>{m.tunnels_adopt_to_finish()}</p>
				<ol>
					<li>{m.tunnels_adopt_step_stop()}</li>
					<li>{m.tunnels_adopt_step_autostart()}</li>
				</ol>
				<p>{m.tunnels_adopt_after_steps()}</p>
			</div>
		</div>

	{:else if step === 'error'}
		<div class="alert alert-error">
			<CircleX size={24} aria-hidden="true" />
			<div>
				<h3>{m.tunnels_adopt_error()}</h3>
				<p>{displayError()}</p>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		{#if step === 'upload'}
			<Button variant="secondary" onclick={handleClose}>{m.common_cancel()}</Button>
			<Button variant="primary" onclick={handleNext} disabled={!configContent}>
				{m.tunnels_adopt_next()}
			</Button>
		{:else if step === 'instructions'}
			<Button variant="secondary" onclick={() => step = 'upload'}>{m.tunnels_back()}</Button>
			<Button variant="primary" onclick={handleConfirm} loading={loading}>
				{m.tunnels_adopt_continue()}
			</Button>
		{:else if step === 'error'}
			<Button variant="secondary" onclick={() => step = 'instructions'}>{m.tunnels_back()}</Button>
		{/if}
	{/snippet}
</Modal>

<style>
	.dialog-description {
		color: var(--text-secondary);
		margin-bottom: 1.25rem;
	}

	.form-group {
		margin-bottom: 1rem;
	}

	.form-group label {
		display: block;
		font-size: 0.875rem;
		font-weight: 500;
		margin-bottom: 0.5rem;
	}

	.form-group input[type="text"] {
		width: 100%;
		padding: 0.5rem 0.75rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font-size: 0.875rem;
	}

	.form-group input[type="text"]:focus {
		outline: none;
		border-color: var(--accent);
	}

	.error-text {
		color: var(--error);
		font-size: 0.875rem;
	}

	.alert {
		display: flex;
		gap: 1rem;
		padding: 1rem;
		border-radius: 8px;
	}

	.alert h3 {
		font-size: 1rem;
		font-weight: 600;
		margin-bottom: 0.5rem;
	}

	.alert ol {
		margin: 0.75rem 0;
		padding-left: 1.25rem;
	}

	.alert li {
		margin: 0.25rem 0;
	}

	.alert-warning {
		background: rgba(224, 175, 104, 0.1);
		color: var(--warning);
	}

	.alert-error {
		background: rgba(247, 118, 142, 0.1);
		color: var(--error);
	}
</style>
