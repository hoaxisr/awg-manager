<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import type { SingboxRouterRuleSet } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		ruleSet?: SingboxRouterRuleSet;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (rs: SingboxRouterRuleSet) => Promise<void> | void;
	}
	let { ruleSet, outboundOptions, onClose, onSave }: Props = $props();

	// svelte-ignore state_referenced_locally
	let type: 'remote' | 'local' = $state(ruleSet?.type ?? 'remote');
	// svelte-ignore state_referenced_locally
	let format: 'binary' | 'source' = $state(ruleSet?.format ?? 'binary');
	// svelte-ignore state_referenced_locally
	let tag = $state(ruleSet?.tag ?? '');
	// svelte-ignore state_referenced_locally
	let url = $state(ruleSet?.url ?? '');
	// svelte-ignore state_referenced_locally
	let updateInterval = $state(ruleSet?.update_interval ?? '24h');
	// svelte-ignore state_referenced_locally
	let downloadDetour = $state(ruleSet?.download_detour ?? '');
	// svelte-ignore state_referenced_locally
	let path = $state(ruleSet?.path ?? '');

	let busy = $state(false);
	let error = $state('');

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			if (!tag.trim()) {
				error = 'Tag обязателен';
				busy = false;
				return;
			}
			if (type === 'remote' && !url.trim()) {
				error = 'URL обязателен для type=remote';
				busy = false;
				return;
			}
			if (type === 'local' && !path.trim()) {
				error = 'Path обязателен для type=local';
				busy = false;
				return;
			}

			const built: SingboxRouterRuleSet = {
				tag: tag.trim(),
				type,
				format,
				url: type === 'remote' ? url.trim() : undefined,
				update_interval: type === 'remote' ? updateInterval : undefined,
				download_detour: type === 'remote' && downloadDetour ? downloadDetour : undefined,
				path: type === 'local' ? path.trim() : undefined,
			};
			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<Modal open onclose={onClose} title={ruleSet ? 'Редактировать rule set' : 'Новый rule set'}>
	<div class="form">
		<div class="section-label">Тип</div>
		<div class="segment">
			<button class:active={type === 'remote'} onclick={() => (type = 'remote')} type="button">Remote</button>
			<button class:active={type === 'local'} onclick={() => (type = 'local')} type="button">Local</button>
		</div>

		<label class="field">
			<div class="lbl">Tag (имя)</div>
			<input bind:value={tag} placeholder="geosite-example" />
		</label>

		<label class="field">
			<div class="lbl">Формат</div>
			<div class="segment">
				<button class:active={format === 'binary'} onclick={() => (format = 'binary')} type="button">Binary (.srs)</button>
				<button class:active={format === 'source'} onclick={() => (format = 'source')} type="button">Source (JSON)</button>
			</div>
		</label>

		{#if type === 'remote'}
			<label class="field">
				<div class="lbl">URL к файлу</div>
				<input bind:value={url} placeholder="https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-example.srs" />
			</label>

			<label class="field">
				<div class="lbl">Интервал обновления</div>
				<select bind:value={updateInterval}>
					<option value="6h">6h</option>
					<option value="12h">12h</option>
					<option value="24h">24h (рекомендуется)</option>
					<option value="168h">168h (неделя)</option>
				</select>
			</label>

			<div class="field highlight">
				<div class="lbl">Скачивать через (download detour)</div>
				<select bind:value={downloadDetour}>
					<option value="">автоматически (direct)</option>
					{#each outboundOptions as group}
						<optgroup label={group.group}>
							{#each group.items as item}
								<option value={item.value}>{item.label}</option>
							{/each}
						</optgroup>
					{/each}
				</select>
				<div class="hint">
					Через какой outbound скачивать этот файл. Полезно если URL заблокирован у провайдера — используйте VPN-туннель.
				</div>
			</div>
		{:else}
			<label class="field">
				<div class="lbl">Путь к файлу</div>
				<input bind:value={path} placeholder="/opt/etc/awg-manager/singbox/rulesets/my-custom.srs" />
				<div class="hint">Абсолютный путь. Файл должен существовать на роутере.</div>
			</label>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}

		<div class="actions">
			<button class="btn btn-secondary" onclick={onClose} type="button">Отмена</button>
			<button class="btn btn-primary" onclick={save} disabled={busy} type="button">Сохранить</button>
		</div>
	</div>
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.6rem;
		min-width: 420px;
	}
	.section-label {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--muted-text);
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.field.highlight {
		padding: 0.6rem;
		background: var(--bg);
		border-left: 2px solid var(--accent, #3b82f6);
		border-radius: 4px;
	}
	.lbl {
		font-size: 0.75rem;
		color: var(--muted-text);
	}
	.hint {
		font-size: 0.75rem;
		color: var(--muted-text);
		line-height: 1.4;
		margin-top: 0.25rem;
	}
	.field input,
	.field select {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.4rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
		width: 100%;
		box-sizing: border-box;
	}
	.segment {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: 4px;
		overflow: hidden;
		width: fit-content;
	}
	.segment button {
		background: transparent;
		border: none;
		padding: 0.4rem 0.9rem;
		font-size: 0.85rem;
		cursor: pointer;
		color: var(--muted-text);
	}
	.segment button + button {
		border-left: 1px solid var(--border);
	}
	.segment button.active {
		background: var(--accent, #3b82f6);
		color: white;
		font-weight: 600;
	}
	.error {
		color: var(--danger, #dc2626);
		font-size: 0.85rem;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
	}
</style>
