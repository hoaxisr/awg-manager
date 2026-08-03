<script lang="ts">
	import { Modal, Input, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';

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
	let error = $state('');
	// Какое поле подсветить: тег или textarea конфига.
	let errorField = $state<'tag' | 'config' | ''>('');
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
		const m = text.match(/^\s*Endpoint\s*=\s*(.+)$/im);
		if (!m) return '';
		const host = m[1].trim();
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
		error = '';
		errorField = '';
		if (!tagTouched) tag = peekDefaultTag(value);
	}

	function onTagInput(): void {
		tagTouched = true;
		error = '';
		errorField = '';
	}

	function reset(): void {
		tag = '';
		jsonText = '';
		tagTouched = false;
		error = '';
		errorField = '';
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
		error = '';
		errorField = '';
		const cleanTag = tag.trim();
		if (cleanTag === '') {
			error = 'Укажите тег';
			errorField = 'tag';
			return;
		}
		const raw = jsonText.trim();
		if (raw === '') {
			error = 'Вставьте конфиг или загрузите .conf';
			errorField = 'config';
			return;
		}
		let config: unknown;
		if (raw.startsWith('{')) {
			try {
				config = JSON.parse(raw);
			} catch (e) {
				error = `Некорректный JSON: ${e instanceof Error ? e.message : 'ошибка разбора'}`;
				errorField = 'config';
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
			error = e instanceof Error ? e.message : 'Не удалось импортировать конфиг';
			errorField = 'config';
		} finally {
			importing = false;
		}
	}
</script>

<Modal {open} title="Импорт endpoint'а" size="md" closeOnBackdrop={false} onclose={requestClose}>
	<div class="import-form">
		<Input
			label="Тег"
			bind:value={tag}
			oninput={onTagInput}
			placeholder="имя туннеля"
			disabled={importing}
			error={errorField === 'tag' ? error : ''}
			fullWidth
		/>

		<div class="field">
			<div class="field-head">
				<label class="field-lbl" for="awg3-conf">Конфиг (JSON или .conf)</label>
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
					Загрузить .conf
				</Button>
			</div>
			<textarea
				id="awg3-conf"
				class="field-textarea"
				class:is-error={errorField === 'config'}
				rows="10"
				spellcheck="false"
				placeholder={'{ "type": "awg", … }  или  [Interface] …'}
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
		<Button variant="ghost" size="md" onclick={requestClose} disabled={importing}>Отмена</Button>
		<Button variant="primary" size="md" onclick={submit} loading={importing} disabled={importing}>
			Импортировать
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
