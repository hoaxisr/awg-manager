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
	let importing = $state(false);

	// Достаёт peers[0].address из вставленного конфига для дефолтного тега.
	// Разворачивает RouteBox-envelope {success,data} так же, как бэкенд Parse,
	// либо читает голый awg-объект.
	function peekDefaultTag(text: string): string {
		const raw = text.trim();
		if (raw === '') return '';
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

	function onJsonInput(value: string): void {
		jsonText = value;
		error = '';
		if (!tagTouched) tag = peekDefaultTag(value);
	}

	function onTagInput(): void {
		tagTouched = true;
		error = '';
	}

	function reset(): void {
		tag = '';
		jsonText = '';
		tagTouched = false;
		error = '';
		importing = false;
	}

	function requestClose(): void {
		reset();
		onclose();
	}

	async function submit(): Promise<void> {
		error = '';
		const cleanTag = tag.trim();
		if (cleanTag === '') {
			error = 'Укажите тег';
			return;
		}
		const raw = jsonText.trim();
		if (raw === '') {
			error = 'Вставьте JSON-конфиг';
			return;
		}
		let config: unknown;
		try {
			config = JSON.parse(raw);
		} catch (e) {
			error = `Некорректный JSON: ${e instanceof Error ? e.message : 'ошибка разбора'}`;
			return;
		}
		importing = true;
		try {
			await api.awg3Import(cleanTag, config);
			onimported();
			requestClose();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Не удалось импортировать конфиг';
		} finally {
			importing = false;
		}
	}
</script>

<Modal {open} title="Импорт AWG3" size="md" closeOnBackdrop={false} onclose={requestClose}>
	<div class="import-form">
		<Input
			label="Тег"
			bind:value={tag}
			oninput={onTagInput}
			placeholder="имя туннеля"
			disabled={importing}
			fullWidth
		/>

		<label class="field">
			<span class="field-lbl">JSON-конфиг</span>
			<textarea
				class="field-textarea"
				class:is-error={!!error}
				rows="10"
				spellcheck="false"
				placeholder={'{ "type": "awg", "private_key": "…", "peers": [ … ] }'}
				disabled={importing}
				value={jsonText}
				oninput={(e) => onJsonInput(e.currentTarget.value)}
			></textarea>
		</label>

		{#if error}
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

	.import-error {
		font-size: 12px;
		color: var(--color-error);
		white-space: pre-wrap;
	}
</style>
