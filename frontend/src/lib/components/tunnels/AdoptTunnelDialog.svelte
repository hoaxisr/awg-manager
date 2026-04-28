<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';

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
			localError = 'Не удалось прочитать файл';
		};
		reader.readAsText(file);
	}

	function handleNext(): void {
		if (!configContent.trim()) {
			localError = 'Загрузите файл конфигурации';
			return;
		}
		localError = '';
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
	title="Взять под управление: {interfaceName}"
	onclose={handleClose}
	size="md"
>
	{#if step === 'upload'}
		<p class="dialog-description">
			Для импорта туннеля загрузите его конфигурационный файл (.conf).
		</p>

		<div class="form-group">
			<label for="config-file">Файл конфигурации</label>
			<input
				type="file"
				id="config-file"
				accept=".conf"
				onchange={handleFileSelect}
			/>
		</div>

		{#if configContent}
			<div class="form-group">
				<label for="tunnel-name">Название туннеля (опционально)</label>
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
			<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
				<line x1="12" y1="9" x2="12" y2="13"/>
				<line x1="12" y1="17" x2="12.01" y2="17"/>
			</svg>
			<div>
				<h3>Важно!</h3>
				<p>Для завершения импорта необходимо:</p>
				<ol>
					<li>Остановить туннель во внешней программе/скрипте</li>
					<li>Отключить автозапуск туннеля (cron, init.d, rc.local и т.д.)</li>
				</ol>
				<p>После выполнения этих действий нажмите «Продолжить».</p>
			</div>
		</div>

	{:else if step === 'error'}
		<div class="alert alert-error">
			<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<circle cx="12" cy="12" r="10"/>
				<line x1="15" y1="9" x2="9" y2="15"/>
				<line x1="9" y1="9" x2="15" y2="15"/>
			</svg>
			<div>
				<h3>Ошибка</h3>
				<p>{displayError()}</p>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		{#if step === 'upload'}
			<Button variant="secondary" onclick={handleClose}>Отмена</Button>
			<Button variant="primary" onclick={handleNext} disabled={!configContent}>
				Далее
			</Button>
		{:else if step === 'instructions'}
			<Button variant="secondary" onclick={() => step = 'upload'}>Назад</Button>
			<Button variant="primary" onclick={handleConfirm} loading={loading}>
				Продолжить
			</Button>
		{:else if step === 'error'}
			<Button variant="secondary" onclick={() => step = 'instructions'}>Назад</Button>
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
