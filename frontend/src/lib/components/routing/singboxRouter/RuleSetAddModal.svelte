<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import { Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { SingboxRouterRuleSet } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		ruleSet?: SingboxRouterRuleSet;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (rs: SingboxRouterRuleSet) => Promise<void> | void;
	}
	let { ruleSet, outboundOptions, onClose, onSave }: Props = $props();

	const UPDATE_INTERVAL_OPTIONS: DropdownOption[] = [
		{ value: '6h', label: '6h' },
		{ value: '12h', label: '12h' },
		{ value: '24h', label: '24h (рекомендуется)' },
		{ value: '168h', label: '168h (неделя)' },
	];

	const transportOptions = $derived<DropdownOption[]>([
		{ value: '', label: 'по умолчанию (route.default_http_client / direct)' },
		...outboundOptions.flatMap((g) =>
			g.items.map((i) => ({ value: i.value, label: i.label, group: g.group })),
		),
	]);

	const isEditing = $derived(Boolean(ruleSet));

	// svelte-ignore state_referenced_locally
	let type: 'remote' | 'local' | 'inline' = $state(ruleSet?.type ?? 'remote');
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
	// svelte-ignore state_referenced_locally
	let rulesJson = $state(
		ruleSet?.rules?.length
			? JSON.stringify(ruleSet.rules, null, 2)
			: `[
  {
    "domain_suffix": [
      ".example.com"
    ]
  }
]`,
	);

	let busy = $state(false);
	let error = $state('');

	function normalizeTag(input: string): string {
		return input.trim();
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const cleanTag = isEditing ? (ruleSet?.tag ?? '') : normalizeTag(tag);
			if (!cleanTag) {
				error = 'Tag обязателен';
				busy = false;
				return;
			}
			if (type === 'remote' && !url.trim()) {
				error = 'URL обязателен для удалённого набора';
				busy = false;
				return;
			}
			if (type === 'local' && !path.trim()) {
				error = 'Абсолютный путь обязателен для локального набора';
				busy = false;
				return;
			}
			if (type === 'local' && !path.trim().startsWith('/')) {
				error = 'Нужен абсолютный путь, начинающийся с /';
				busy = false;
				return;
			}

			let parsedRules: Record<string, unknown>[] | undefined;
			if (type === 'inline') {
				try {
					const parsed = JSON.parse(rulesJson);
					if (!Array.isArray(parsed) || parsed.length === 0) {
						error = 'Для inline rule-set нужен непустой JSON-массив правил';
						busy = false;
						return;
					}
					parsedRules = parsed as Record<string, unknown>[];
				} catch (e) {
					error = `Не удалось разобрать JSON inline-правил: ${(e as Error).message}`;
					busy = false;
					return;
				}
			}

			const built: SingboxRouterRuleSet = {
				tag: cleanTag,
				type,
				format: type === 'inline' ? undefined : format,
				url: type === 'remote' ? url.trim() : undefined,
				update_interval: type === 'remote' ? updateInterval : undefined,
				download_detour: type === 'remote' && downloadDetour ? downloadDetour : undefined,
				path: type === 'local' ? path.trim() : undefined,
				rules: type === 'inline' ? parsedRules : undefined,
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
		<div class="section-label">Тип источника</div>
		<div class="segment">
			<button class:active={type === 'remote'} onclick={() => (type = 'remote')} type="button">Remote</button>
			<button class:active={type === 'local'} onclick={() => (type = 'local')} type="button">Local</button>
			<button class:active={type === 'inline'} onclick={() => (type = 'inline')} type="button">Inline</button>
		</div>

		<label class="field">
			<div class="lbl">Tag (внутреннее имя)</div>
			<input bind:value={tag} placeholder="geosite-youtube" disabled={isEditing} />
			<div class="hint">
				{#if isEditing}
					Tag уже используется в правилах, поэтому при редактировании мы его не меняем.
				{:else}
					Используйте устойчивое имя: по нему rule set будет ссылаться в маршрутах и DNS-правилах.
				{/if}
			</div>
		</label>

		{#if type !== 'inline'}
			<label class="field">
				<div class="lbl">Формат</div>
				<div class="segment">
					<button class:active={format === 'binary'} onclick={() => (format = 'binary')} type="button">Binary (.srs)</button>
					<button class:active={format === 'source'} onclick={() => (format = 'source')} type="button">Source (JSON)</button>
				</div>
			</label>
		{/if}

		{#if type === 'remote'}
			<label class="field">
				<div class="lbl">URL к набору</div>
				<input bind:value={url} placeholder="https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-youtube.srs" />
				<div class="hint">
					Для текущих версий sing-box предпочтительнее <code>.srs</code> rule-set файлы.
				</div>
			</label>

			<label class="field">
				<div class="lbl">Интервал обновления</div>
				<Dropdown bind:value={updateInterval} options={UPDATE_INTERVAL_OPTIONS} fullWidth />
			</label>

			<div class="field highlight">
				<div class="lbl">HTTP client / скачивать через</div>
				<Dropdown bind:value={downloadDetour} options={transportOptions} fullWidth />
				<div class="hint">
					Стабильный режим для remote rule-set: <code>download_detour</code>. Это безопасно и совместимо
					с текущими сборками sing-box на устройствах.
				</div>
			</div>
		{:else if type === 'local'}
			<label class="field">
				<div class="lbl">Путь к локальному файлу</div>
				<input bind:value={path} placeholder="/opt/etc/awg-manager/singbox/rulesets/custom.srs" />
				<div class="hint">
					Нужен абсолютный путь на роутере. sing-box будет читать этот файл напрямую.
				</div>
			</label>
		{:else}
			<label class="field">
				<div class="lbl">Inline headless rules (JSON array)</div>
				<textarea bind:value={rulesJson} rows="12" placeholder='[&#123;"domain_suffix":[".example.com"]&#125;]'></textarea>
				<div class="hint">
					Inline rule-set полезен для небольших локальных списков, которые вы хотите хранить прямо в
					<code>20-router.json</code> без внешнего файла.
				</div>
			</label>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}

		<div class="actions">
			<button class="btn btn-secondary" onclick={onClose} type="button">Отмена</button>
			<button class="btn btn-primary" onclick={save} disabled={busy} type="button">
				{isEditing ? 'Сохранить изменения' : 'Создать набор'}
			</button>
		</div>
	</div>
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.7rem;
		min-width: 0;
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
		padding: 0.7rem;
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
		margin-top: 0.2rem;
	}
	.hint code {
		background: var(--bg);
		padding: 0.05rem 0.25rem;
		border-radius: 2px;
		font-family: ui-monospace, monospace;
	}
	.field input,
	.field textarea {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.45rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
		width: 100%;
		box-sizing: border-box;
	}
	.field textarea {
		resize: vertical;
	}
	.segment {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: 4px;
		overflow: hidden;
		width: fit-content;
		flex-wrap: wrap;
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
