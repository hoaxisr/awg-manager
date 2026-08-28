<script lang="ts">
	import type { SystemServiceItem } from '$lib/api/client';
	import type { CreateServiceFormState } from './createService';

	interface Props {
		form: CreateServiceFormState;
		/** Список служб-доноров для режима клонирования. */
		items: SystemServiceItem[];
		/** Предпросмотр init.d-скрипта для режима конструктора. */
		generatedScript: string;
	}

	let { form = $bindable(), items, generatedScript }: Props = $props();
</script>

{#if form.mode === 'template'}
	<!-- Template Mode -->
	<div class="form-grid">
		<div class="form-row">
			<label class="form-field">
				<span class="field-label">Имя службы <span class="required">*</span>:</span>
				<input
					type="text"
					placeholder="например: my-proxy, qwdtt, xray"
					bind:value={form.tplName}
				/>
				<span class="field-hint">Используется в названии скрипта: <code>/opt/etc/init.d/S{form.tplPriority || 90}{form.tplName || 'name'}</code></span>
			</label>

			<label class="form-field" style="max-width: 140px;">
				<span class="field-label">Приоритет (S10-S99):</span>
				<input
					type="number"
					min="10"
					max="99"
					bind:value={form.tplPriority}
				/>
				<span class="field-hint">Порядок автозапуска</span>
			</label>
		</div>

		<div class="form-row">
			<label class="form-field">
				<span class="field-label">Имя процесса / бинарника:</span>
				<input
					type="text"
					placeholder={form.tplName ? form.tplName : 'например: qwdtt или /opt/bin/sing-box'}
					bind:value={form.tplProc}
				/>
				<span class="field-hint">Переменная PROCS для отслеживания PID через rc.func</span>
			</label>

			<label class="form-field">
				<span class="field-label">Описание службы:</span>
				<input
					type="text"
					placeholder="например: Мой прокси-сервер"
					bind:value={form.tplDesc}
				/>
			</label>
		</div>

		<label class="form-field">
			<span class="field-label">Аргументы и ключи запуска (ARGS):</span>
			<input
				type="text"
				placeholder="например: -c /opt/etc/config.json -log /opt/var/log/my.log"
				bind:value={form.tplArgs}
			/>
		</label>

		<!-- Preview -->
		<div class="preview-box">
			<span class="preview-label">Сгенерированный init.d скрипт:</span>
			<pre class="code-preview"><code>{generatedScript}</code></pre>
		</div>
	</div>
{:else if form.mode === 'clone'}
	<!-- Clone Mode -->
	<div class="form-grid">
		<label class="form-field">
			<span class="field-label">Выберите исходную службу-донор:</span>
			<select bind:value={form.cloneSourceScript}>
				{#each items as it}
					<option value={it.script}>{it.name} ({it.script})</option>
				{/each}
			</select>
		</label>

		<div class="form-row">
			<label class="form-field">
				<span class="field-label">Имя новой службы <span class="required">*</span>:</span>
				<input
					type="text"
					placeholder="например: my-service-2"
					bind:value={form.cloneTargetName}
				/>
			</label>

			<label class="form-field" style="max-width: 140px;">
				<span class="field-label">Приоритет:</span>
				<input
					type="number"
					min="10"
					max="99"
					bind:value={form.clonePriority}
				/>
			</label>
		</div>
	</div>
{:else}
	<!-- Custom Mode -->
	<div class="form-grid">
		<label class="form-field">
			<span class="field-label">Имя файла в /opt/etc/init.d/ <span class="required">*</span>:</span>
			<input
				type="text"
				placeholder="S90custom-daemon"
				bind:value={form.customScriptName}
			/>
		</label>

		<label class="form-field">
			<span class="field-label">Код init-скрипта:</span>
			<textarea
				rows="12"
				class="code-textarea"
				bind:value={form.customScriptContent}
			></textarea>
		</label>
	</div>
{/if}

<style>
	.form-grid {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.form-row {
		display: flex;
		gap: 0.75rem;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		flex: 1;
	}

	.field-label {
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}
	.required {
		color: var(--color-error, #f87171);
	}

	.form-field input, .form-field select {
		padding: 0.4rem 0.55rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.82rem;
	}
	.form-field input:focus, .form-field select:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.field-hint {
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}
	.field-hint code {
		color: var(--color-accent);
	}

	.preview-box {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		margin-top: 0.25rem;
	}
	.preview-label {
		font-size: 0.74rem;
		font-weight: 600;
		color: var(--color-text-muted);
	}
	.code-preview {
		margin: 0;
		padding: 0.6rem;
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		font-size: 0.76rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-primary);
		max-height: 180px;
		overflow-y: auto;
	}

	.code-textarea {
		width: 100%;
		box-sizing: border-box;
		padding: 0.6rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-family: var(--font-mono, monospace);
		font-size: 0.8rem;
		line-height: 1.4;
		resize: vertical;
	}
	.code-textarea:focus {
		border-color: var(--color-accent);
		outline: none;
	}
</style>
