<script lang="ts">
	import { Modal, Input, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';

	interface Props {
		open: boolean;
		onclose: () => void;
		onimported: () => void;
	}

	let { open, onclose, onimported }: Props = $props();

	let tag = $state('');
	let jsonText = $state('');
	// Тег авто-подставляется из JSON, пока пользователь сам его не тронул.
	let tagTouched = $state(false);
	// Ошибка хранится как код (+ текст бэкенда), а не как переведённая строка:
	// подпись собирается при отрисовке и следует за языком интерфейса.
	type ImportError =
		| { kind: 'tag-required' }
		| { kind: 'config-required' }
		| { kind: 'invalid-json'; detail: string | null }
		| { kind: 'import-failed'; message: string | null };
	let importError = $state<ImportError | null>(null);
	// Какое поле подсветить: тег или textarea конфига.
	const errorField = $derived<'tag' | 'config' | ''>(
		importError === null ? '' : importError.kind === 'tag-required' ? 'tag' : 'config'
	);
	const error = $derived.by((): string => {
		if (importError === null) return '';
		switch (importError.kind) {
			case 'tag-required':
				return m.awg3_import_tag_required();
			case 'config-required':
				return m.awg3_import_config_required();
			case 'invalid-json':
				return m.awg3_import_invalid_json({
					detail: importError.detail ?? m.awg3_import_parse_error()
				});
			case 'import-failed':
				return importError.message ?? m.awg3_import_failed();
		}
	});
	let importing = $state(false);

	// Достаёт peers[0].address из вставленного конфига для дефолтного тега.
	// Разворачивает RouteBox-envelope {success,data} так же, как бэкенд Parse,
	// либо читает голый awg-объект.
	function peekDefaultTag(text: string): string {
		const raw = text.trim();
		if (raw === '') return '';
		if (!raw.startsWith('{')) return peekConfTag(raw);
		let parsed: unknown;
		try {
			parsed = JSON.parse(raw);
		} catch {
			return '';
		}
		if (!parsed || typeof parsed !== 'object') return '';
		const rec = parsed as Record<string, unknown>;
		const data =
			rec.data && typeof rec.data === 'object' ? (rec.data as Record<string, unknown>) : rec;
		const peers = data.peers;
		if (!Array.isArray(peers) || peers.length === 0) return '';
		const first = peers[0];
		if (!first || typeof first !== 'object') return '';
		const addr = (first as Record<string, unknown>).address;
		return typeof addr === 'string' ? addr.trim() : '';
	}

	// Дефолт-тег из строки Endpoint = host:port нативного .conf (host).
	function peekConfTag(text: string): string {
		const endpoint = text.match(/^\s*Endpoint\s*=\s*(.+)$/im);
		if (!endpoint) return '';
		const host = endpoint[1].trim();
		if (host.startsWith('[')) {
			const end = host.indexOf(']');
			return end > 0 ? host.slice(1, end) : '';
		}
		const colon = host.lastIndexOf(':');
		return colon > 0 ? host.slice(0, colon) : host;
	}

	let fileInput = $state<HTMLInputElement | null>(null);

	async function onFilePick(e: Event): Promise<void> {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		input.value = ''; // сброс, чтобы повторный выбор того же файла дал change
		if (!file) return;
		onJsonInput(await file.text());
	}

	function onJsonInput(value: string): void {
		jsonText = value;
		importError = null;
		if (!tagTouched) tag = peekDefaultTag(value);
	}

	function onTagInput(): void {
		tagTouched = true;
		importError = null;
	}

	function reset(): void {
		tag = '';
		jsonText = '';
		tagTouched = false;
		importError = null;
		importing = false;
	}

	function requestClose(): void {
		// Пока запрос летит, модалку закрывать нельзя — иначе reset() затрёт
		// поля, а завершившийся импорт впишет stale-ошибку в уже сброшенную форму.
		if (importing) return;
		reset();
		onclose();
	}

	async function submit(): Promise<void> {
		if (importing) return;
		importError = null;
		const cleanTag = tag.trim();
		if (cleanTag === '') {
			importError = { kind: 'tag-required' };
			return;
		}
		const raw = jsonText.trim();
		if (raw === '') {
			importError = { kind: 'config-required' };
			return;
		}
		let config: unknown;
		if (raw.startsWith('{')) {
			try {
				config = JSON.parse(raw);
			} catch (e) {
				importError = { kind: 'invalid-json', detail: e instanceof Error ? e.message : null };
				return;
			}
		} else {
			config = raw; // .conf-текст — уйдёт строкой, backend распарсит
		}
		importing = true;
		try {
			await api.awg3Import(cleanTag, config);
			// Снять флаг до requestClose(): его гард `if (importing) return`
			// иначе съел бы закрытие (finally оставит false — идемпотентно).
			importing = false;
			onimported();
			requestClose();
		} catch (e) {
			importError = { kind: 'import-failed', message: e instanceof Error ? e.message : null };
		} finally {
			importing = false;
		}
	}
</script>

<Modal {open} title={m.awg3_import_title()} size="md" closeOnBackdrop={false} onclose={requestClose}>
	<div class="import-form">
		<Input
			label={m.awg3_import_tag_label()}
			bind:value={tag}
			oninput={onTagInput}
			placeholder={m.awg3_import_tag_placeholder()}
			disabled={importing}
			error={errorField === 'tag' ? error : ''}
			fullWidth
		/>

		<div class="field">
			<div class="field-head">
				<label class="field-lbl" for="awg3-conf">{m.awg3_import_config_label()}</label>
				<input
					type="file"
					accept=".conf,.txt"
					bind:this={fileInput}
					onchange={onFilePick}
					hidden
				/>
				<Button
					variant="ghost"
					size="sm"
					onclick={() => fileInput?.click()}
					disabled={importing}
				>
					{m.awg3_import_upload_conf()}
				</Button>
			</div>
			<textarea
				id="awg3-conf"
				class="field-textarea"
				class:is-error={errorField === 'config'}
				rows="10"
				spellcheck="false"
				placeholder={m.awg3_import_config_placeholder({ json: '{ "type": "awg", … }' })}
				disabled={importing}
				value={jsonText}
				oninput={(e) => onJsonInput(e.currentTarget.value)}
			></textarea>
		</div>

		{#if error && errorField !== 'tag'}
			<div class="import-error">{error}</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={requestClose} disabled={importing}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={submit} loading={importing} disabled={importing}>
			{m.awg3_import_submit()}
		</Button>
	{/snippet}
</Modal>

<style>
	.import-form {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.field-lbl {
		font-size: 13px;
		color: var(--color-text-secondary);
		font-weight: 500;
	}

	.field-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.import-error {
		font-size: 12px;
		color: var(--color-error);
		white-space: pre-wrap;
	}
</style>
