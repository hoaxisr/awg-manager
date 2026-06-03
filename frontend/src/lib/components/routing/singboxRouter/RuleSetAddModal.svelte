<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import { Button, Dropdown, SyntaxHighlightedTextarea, type DropdownOption } from '$lib/components/ui';
	import { highlightJson } from '$lib/utils/shareEditorHighlight';
	import { api } from '$lib/api/client';
	import type { GeoFileEntry, SingboxRouterRuleSet } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import {
		analyzeInlineRuleListLossy,
		isInlineRuleListEmpty,
		parseInlineRuleList,
		stringifyInlineRuleList,
		validateRuleSetTag,
	} from '$lib/utils/singboxInlineRules';
	import { expandGeoLinesInInput } from '$lib/utils/singboxInlineGeoExpand';
	import InlineRuleListEditor from './InlineRuleListEditor.svelte';
	import GeoTagPicker from './GeoTagPicker.svelte';

	interface Props {
		ruleSet?: SingboxRouterRuleSet;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (rs: SingboxRouterRuleSet) => Promise<void> | void;
	}
	let { ruleSet, outboundOptions, onClose, onSave }: Props = $props();

	// ── constants ───────────────────────────────────────────────
	const UPDATE_INTERVAL_OPTIONS: DropdownOption[] = [
		{ value: '6h', label: '6h' },
		{ value: '12h', label: '12h' },
		{ value: '24h', label: '24h (рекомендуется)' },
		{ value: '168h', label: '168h (неделя)' },
	];

	// ── derived ────────────────────────────────────────────────
	const downloadDetourOptions = $derived<DropdownOption[]>([
		{ value: '', label: 'автоматически (direct)' },
		...outboundOptions.flatMap((g) =>
			g.items.map((i) => ({ value: i.value, label: i.label, group: g.group })),
		),
	]);

	const isEditing = $derived(Boolean(ruleSet));

	// ── line numbers for JSON editor ─────────────────────
	function lineNumbersFor(text: string): string {
		const count = Math.max(1, text.split(/\r?\n/).length);
		return Array.from({ length: count }, (_, i) => String(i + 1)).join('\n');
	}

	let rulesJsonTextarea = $state<HTMLTextAreaElement | null>(null);
	let rulesJsonLineNumberGutter = $state<HTMLPreElement | null>(null);

	function syncRulesJsonLineNumbersScroll(): void {
		if (!rulesJsonTextarea || !rulesJsonLineNumberGutter) return;
		rulesJsonLineNumberGutter.scrollTop = rulesJsonTextarea.scrollTop;
	}

	// ── form state ──────────────────────────────────────────────
	// For inline rule sets: 'list' = smart-line-by-line, 'json' = raw JSON array
	let inlineMode: 'list' | 'json' = $state('list');

	let rulesList = $state('');

	type RuleSetFormType = 'remote' | 'local' | 'inline' | 'geosite' | 'geoip';

	let type = $state<RuleSetFormType>('remote');
	let format: 'binary' | 'source' = $state('binary');
	let tag = $state('');
	let url = $state('');
	let updateInterval = $state('24h');
	let downloadDetour = $state('');
	let path = $state('');
	let rulesJson = $state('');
	let geoFiles = $state<GeoFileEntry[]>([]);
	let selectedGeoTags = $state<string[]>([]);
	let geoFilesLoading = $state(false);

	const rulesJsonLineNumbers = $derived(lineNumbersFor(rulesJson));

	let busy = $state(false);
	let error = $state('');
	let inlineModeBusy = $state(false);
	/** Geo/list-parse or JSON→list serializer messages shown after switching Список ↔ JSON */
	let inlineTabConvertWarnings = $state<string[]>([]);

	async function switchInlineMode(next: 'list' | 'json'): Promise<void> {
		if (next === inlineMode || inlineModeBusy) return;
		error = '';
		inlineTabConvertWarnings = [];
		if (type !== 'inline') {
			inlineMode = next;
			return;
		}
		inlineModeBusy = true;
		try {
			if (next === 'json' && inlineMode === 'list') {
				if (isInlineRuleListEmpty(rulesList)) {
					rulesJson = DEFAULT_RULES_JSON;
					inlineMode = 'json';
					return;
				}

				const { text: expanded, warnings: geoWarn } = await expandGeoLinesInInput(
					rulesList,
					async (kind, tag) => {
						const res = await api.expandGeoTag(kind, tag);
						return res.lines;
					},
				);
				const parsed = parseInlineRuleList(expanded);
				if (parsed.errors.length > 0) {
					error = parsed.errors.join('\n');
					return;
				}
				rulesJson =
					parsed.rules.length === 0 ? '[]' : JSON.stringify(parsed.rules, null, 2);
				inlineTabConvertWarnings = [...geoWarn, ...parsed.warnings];
				inlineMode = 'json';
				return;
			}
			if (next === 'list' && inlineMode === 'json') {
				const trimmed = rulesJson.trim();
				if (trimmed === '') {
					rulesList = '';
					inlineMode = 'list';
					return;
				}
				let arr: unknown;
				try {
					arr = JSON.parse(rulesJson);
				} catch (e) {
					error = `Некорректный JSON: ${(e as Error).message}`;
					return;
				}
				if (!Array.isArray(arr)) {
					error = 'Правила должны быть JSON-массивом';
					return;
				}
				const typed = arr as Record<string, unknown>[];
				rulesList = stringifyInlineRuleList(typed);
				const lossy = analyzeInlineRuleListLossy(typed);
				inlineTabConvertWarnings = lossy.issues;
				inlineMode = 'list';
				return;
			}
			inlineMode = next;
		} finally {
			inlineModeBusy = false;
		}
	}

	$effect(() => {
		if (type !== 'inline') inlineTabConvertWarnings = [];
	});

	$effect(() => {
		if (type !== 'geosite' && type !== 'geoip') return;
		void (async () => {
			geoFilesLoading = true;
			try {
				geoFiles = (await api.getGeoFiles()) ?? [];
			} catch {
				geoFiles = [];
			} finally {
				geoFilesLoading = false;
			}
		})();
	});

	const isDatType = $derived(type === 'geosite' || type === 'geoip');
	const datKind = $derived(type === 'geoip' ? 'geoip' : 'geosite');
	const datFiles = $derived(
		isDatType ? geoFiles.filter((g) => g.type === datKind).map((g) => g.path) : [],
	);

	const inlineLossyAnalysis = $derived.by(() => {
		if (!ruleSet || ruleSet.type !== 'inline') return { lossy: false, issues: [] as string[] };
		return analyzeInlineRuleListLossy(ruleSet.rules);
	});

	// Default rulesJson template for new rule sets (must match $state initializer above)
	const DEFAULT_RULES_JSON = `[
  {
    "domain_suffix": [
      "example.com"
    ]
  }
]`;

	// Snapshot initial state for isDirty detection
	let initialInlineMode: 'list' | 'json' = $state('list');
	let initialType: RuleSetFormType = $state('remote');
	let initialFormat: 'binary' | 'source' = $state('binary');
	let initialTag = $state('');
	let initialUrl = $state('');
	let initialUpdateInterval = $state('24h');
	let initialDownloadDetour = $state('');
	let initialPath = $state('');
	let initialRulesList = $state('');
	let initialRulesJson = $state('');
	let initialSelectedGeoTags = $state<string[]>([]);

	const formResetKey = $derived(`${isEditing ? 'edit' : 'create'}:${ruleSet?.tag ?? ''}`);

	$effect(() => {
		void formResetKey;

		const datInfo = ruleSet?.type === 'remote' ? datInfoFromUrl(ruleSet.url) : null;
		const nextType: RuleSetFormType = datInfo?.kind ?? ruleSet?.type ?? 'remote';
		const nextFormat: 'binary' | 'source' = ruleSet?.format ?? 'binary';
		const nextTag = ruleSet?.tag ?? '';
		const nextUrl = ruleSet?.url ?? '';
		const nextUpdateInterval = ruleSet?.update_interval ?? '24h';
		const nextDownloadDetour = ruleSet?.download_detour ?? '';
		const nextPath = ruleSet?.path ?? '';
		const nextRulesJson = ruleSet?.rules?.length
			? JSON.stringify(ruleSet.rules, null, 2)
			: DEFAULT_RULES_JSON;
		const nextLossyAnalysis =
			ruleSet?.type === 'inline'
				? analyzeInlineRuleListLossy(ruleSet.rules)
				: { lossy: false, issues: [] as string[] };
		const nextInlineMode: 'list' | 'json' =
			ruleSet?.type === 'inline' && ruleSet?.rules?.length && nextLossyAnalysis.lossy
				? 'json'
				: 'list';
		const nextRulesList =
			nextType === 'inline' && ruleSet?.rules?.length
				? stringifyInlineRuleList(ruleSet.rules)
				: '';

		type = nextType;
		format = nextFormat;
		tag = nextTag;
		url = nextUrl;
		updateInterval = nextUpdateInterval;
		downloadDetour = nextDownloadDetour;
		path = nextPath;
		rulesJson = nextRulesJson;
		inlineMode = nextInlineMode;
		rulesList = nextRulesList;
		selectedGeoTags = datInfo?.tags ?? [];

		initialType = nextType;
		initialFormat = nextFormat;
		initialTag = nextTag;
		initialUrl = nextUrl;
		initialUpdateInterval = nextUpdateInterval;
		initialDownloadDetour = nextDownloadDetour;
		initialPath = nextPath;
		initialRulesJson = nextRulesJson;
		initialInlineMode = nextInlineMode;
		initialRulesList = nextRulesList;
		initialSelectedGeoTags = datInfo?.tags ?? [];

		error = '';
		busy = false;
		inlineTabConvertWarnings = [];
	});

	const isDirty = $derived.by(() => {
		return (
			type !== initialType ||
			format !== initialFormat ||
			tag !== initialTag ||
			url !== initialUrl ||
			updateInterval !== initialUpdateInterval ||
			downloadDetour !== initialDownloadDetour ||
			path !== initialPath ||
			inlineMode !== initialInlineMode ||
			rulesList !== initialRulesList ||
			rulesJson !== initialRulesJson ||
			selectedGeoTags.join('\n') !== initialSelectedGeoTags.join('\n')
		);
	});

	function toggleGeoTag(pickedTag: string): void {
		if ((type !== 'geosite' && type !== 'geoip') || !pickedTag.trim()) return;
		const shouldUpdateRuleSetTag =
			tag.trim() === '' ||
			((type === 'geosite' || type === 'geoip') &&
				selectedGeoTags.length > 0 &&
				tag.trim() === standardDatRuleSetTag(type, selectedGeoTags));
		selectedGeoTags = selectedGeoTags.includes(pickedTag)
			? selectedGeoTags.filter((t) => t !== pickedTag)
			: [...selectedGeoTags, pickedTag];
		if (shouldUpdateRuleSetTag) tag = selectedGeoTags.length > 0 ? standardDatRuleSetTag(type, selectedGeoTags) : '';
	}

	function setType(next: RuleSetFormType): void {
		const wasDatKind = type === 'geosite' || type === 'geoip' ? type : null;
		type = next;
		if ((next === 'geosite' || next === 'geoip') && wasDatKind && wasDatKind !== next) {
			if (tag.trim() === standardDatRuleSetTag(wasDatKind, selectedGeoTags)) tag = '';
			selectedGeoTags = [];
		}
	}

	function standardDatRuleSetTag(kind: 'geosite' | 'geoip', pickedTags: string[]): string {
		return `${kind}-${pickedTags.join('-')}`.toLowerCase().replace(/[^a-z0-9._-]+/g, '-');
	}

	function savedRuleSetType(t: RuleSetFormType): SingboxRouterRuleSet['type'] {
		return t === 'geosite' || t === 'geoip' ? 'remote' : t;
	}

	function datInfoFromUrl(rawUrl?: string): { kind: 'geosite' | 'geoip'; tags: string[] } | null {
		if (!rawUrl) return null;
		try {
			const u = new URL(rawUrl);
			if (u.pathname !== '/api/singbox/router/rulesets/dat-srs') return null;
			const kind = u.searchParams.get('kind');
			const tags = u.searchParams.getAll('tag').filter((t) => t.trim() !== '');
			if ((kind !== 'geosite' && kind !== 'geoip') || tags.length === 0) return null;
			return { kind, tags };
		} catch {
			return null;
		}
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const cleanTag = tag.trim();
			const tagErr = validateRuleSetTag(cleanTag);
			if (tagErr) {
				error = tagErr;
				busy = false;
				return;
			}
			if (isDatType && selectedGeoTags.length === 0) {
				error = 'Выберите хотя бы один тег из dat-файла';
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

			let parsedRules: Record<string, unknown>[] | undefined;
			if (type === 'inline') {
				if (isEditing && inlineMode === 'list' && inlineLossyAnalysis.lossy) {
					error = 'Этот JSON содержит поля, которые режим Список не может сохранить без потерь. Редактируйте в JSON.';
					busy = false;
					return;
				}
				if (inlineMode === 'json') {
					try {
						const parsed = JSON.parse(rulesJson);
						if (!Array.isArray(parsed) || parsed.length === 0) {
							error = 'Для inline rule set нужен непустой JSON-массив правил';
							busy = false;
							return;
						}
						parsedRules = parsed as Record<string, unknown>[];
					} catch (e) {
						error = `Некорректный JSON: ${(e as Error).message}`;
						busy = false;
						return;
					}
				} else {
					if (isInlineRuleListEmpty(rulesList)) {
						error = 'Список пуст';
						busy = false;
						return;
					}
					const { text: expanded, warnings: geoWarn } = await expandGeoLinesInInput(
						rulesList,
						async (kind, tag) => (await api.expandGeoTag(kind, tag)).lines,
					);
					const parsed = parseInlineRuleList(expanded);
					if (parsed.errors.length > 0) {
						error = parsed.errors.join('\n');
						busy = false;
						return;
					}
					if (parsed.rules.length === 0) {
						error = 'Нет валидных строк для inline rule set';
						busy = false;
						return;
					}
					parsedRules = parsed.rules;
				}
			}

			let builtUrl = url.trim();
			if (isDatType) {
				const res = await api.singboxRouterDatRuleSetURL(datKind, selectedGeoTags);
				builtUrl = res.url;
			}

			const savedType = savedRuleSetType(type);
			const built: SingboxRouterRuleSet = {
				tag: cleanTag,
				type: savedType,
				format: savedType === 'inline' ? undefined : isDatType ? 'binary' : format,
				url: savedType === 'remote' ? builtUrl : undefined,
				update_interval: savedType === 'remote' ? (isDatType ? '24h' : updateInterval) : undefined,
				download_detour: type === 'remote' && downloadDetour ? downloadDetour : undefined,
				path: savedType === 'local' ? path.trim() : undefined,
				rules: savedType === 'inline' ? parsedRules : undefined,
			};
			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<Modal open onclose={onClose} title={ruleSet ? 'Редактировать rule set' : 'Новый rule set'} hasUnsavedChanges={() => isDirty}>
	<div class="form">
		<div class="field">
			<div class="lbl">Тип</div>
			<div class="segment">
				<button class:active={type === 'remote'} onclick={() => setType('remote')} type="button">Remote</button>
				<button class:active={type === 'local'} onclick={() => setType('local')} type="button">Local</button>
				<button class:active={type === 'inline'} onclick={() => setType('inline')} type="button">Inline</button>
				<button class:active={type === 'geosite'} onclick={() => setType('geosite')} type="button">Geosite</button>
				<button class:active={type === 'geoip'} onclick={() => setType('geoip')} type="button">GeoIP</button>
			</div>
		</div>

		<label class="field">
			<div class="lbl">Tag (имя)</div>
			<input bind:value={tag} placeholder="geosite-example" />
			{#if isEditing}
				<div class="hint">При переименовании ссылки в правилах маршрутизации и DNS обновятся автоматически.</div>
			{:else}
				<div class="hint">Не используйте суффикс -srs — он добавляется автоматически для скомпилированного набора.</div>
			{/if}
		</label>

		{#if type !== 'inline' && !isDatType}
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
				<div class="lbl">URL к файлу</div>
				<input bind:value={url} placeholder="https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-example.srs" />
			</label>

			<label class="field">
				<div class="lbl">Интервал обновления</div>
				<Dropdown bind:value={updateInterval} options={UPDATE_INTERVAL_OPTIONS} fullWidth />
			</label>

			<div class="field highlight">
				<div class="lbl">Скачивать через (download detour)</div>
				<Dropdown bind:value={downloadDetour} options={downloadDetourOptions} fullWidth />
				<div class="hint">
					Через какой outbound скачивать этот файл. Полезно если URL заблокирован у провайдера — используйте VPN-туннель.
				</div>
			</div>
		{:else if isDatType}
			<div class="field dat-picker-field">
				<div class="dat-picker-head">
					<div>
						<div class="lbl">Теги {type}.dat</div>
						<div class="hint">После сохранения будет создан remote rule-set с локальным URL конвертации в .srs.</div>
					</div>
				</div>
				{#if selectedGeoTags.length > 0}
					<div class="selected-geo-list" aria-label="Выбранные {type} теги">
						{#each selectedGeoTags as geoTag}
							<button type="button" class="selected-geo" onclick={() => toggleGeoTag(geoTag)}>
								<code>{datKind}:{geoTag}</code>
								<span aria-hidden="true">×</span>
							</button>
						{/each}
					</div>
				{:else if geoFilesLoading}
					<div class="hint">Загрузка dat-файлов…</div>
				{:else if datFiles.length === 0}
					<div class="hint">Нет известных файлов {type}.dat. Добавьте их на вкладке «Маршрутизация → Гео-данные».</div>
				{/if}
				{#if datFiles.length > 0}
					<GeoTagPicker
						kind={datKind}
						files={datFiles}
						selected={selectedGeoTags}
						onToggle={toggleGeoTag}
					/>
				{/if}
				<div class="hint">remote · binary .srs · 24h · direct</div>
			</div>
		{:else if type === 'local'}
			<label class="field">
				<div class="lbl">Путь к файлу</div>
				<input bind:value={path} placeholder="/opt/etc/awg-manager/singbox/config.d/rule-sets/my.srs" />
				<div class="hint">Абсолютный путь. Файл должен существовать на роутере.</div>
			</label>
		{:else}
			<div class="field">
				<div class="lbl">Формат ввода</div>
				<div class="segment">
					<button
						class:active={inlineMode === 'list'}
						disabled={inlineModeBusy}
						onclick={() => void switchInlineMode('list')}
						type="button"
					>
						Список
					</button>
					<button
						class:active={inlineMode === 'json'}
						disabled={inlineModeBusy}
						onclick={() => void switchInlineMode('json')}
						type="button"
					>
						JSON
					</button>
				</div>
				{#if inlineTabConvertWarnings.length > 0}
					<div class="parse-messages parse-messages-warning">
						<div class="parse-messages-title">При переключении режима</div>
						<ul>
							{#each inlineTabConvertWarnings as msg}
								<li>{msg}</li>
							{/each}
						</ul>
					</div>
				{/if}
			</div>

			{#if inlineMode === 'list'}
				<div class="field">
					{#if isEditing && inlineLossyAnalysis.lossy}
						<div class="parse-messages parse-messages-warning">
							<div class="parse-messages-title">Потеря данных в режиме "Список"</div>
							<ul>
								<li>Этот JSON содержит поля, которые режим Список не может сохранить без потерь. Редактируйте в JSON.</li>
								{#each inlineLossyAnalysis.issues as issue}
									<li>{issue}</li>
								{/each}
							</ul>
						</div>
					{/if}
					<InlineRuleListEditor bind:value={rulesList} />
				</div>
			{:else}
				<label class="field">
					<div class="lbl">Правила (JSON-массив)</div>
					<div class="rules-editor rules-json-editor">
						<pre
							class="line-numbers"
							aria-hidden="true"
							bind:this={rulesJsonLineNumberGutter}
						>{rulesJsonLineNumbers}</pre>
						<div class="rules-editor-input">
							<SyntaxHighlightedTextarea
								bind:value={rulesJson}
								bind:textareaRef={rulesJsonTextarea}
								highlight={highlightJson}
								indentMode="json"
								wrap="pre-wrap"
								class="rules-json-ta"
								onscroll={syncRulesJsonLineNumbersScroll}
							/>
						</div>
					</div>
					<div class="hint">
						Advanced mode: массив объектов с матчерами sing-box.
					</div>
				</label>
			{/if}
		{/if}

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button
			variant="primary"
			size="md"
			onclick={save}
			disabled={busy || inlineModeBusy}
			loading={busy || inlineModeBusy}
			type="button"
		>
			Сохранить
		</Button>
	{/snippet}
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.6rem;
		min-width: 0;
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
	.dat-picker-field {
		padding: 0.6rem;
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 6px;
	}
	.dat-picker-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
	}
	.selected-geo-list {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
	}
	.selected-geo {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
		padding: 0.35rem 0.5rem;
		border-radius: 4px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		font-size: 0.78rem;
		color: var(--muted-text);
		cursor: pointer;
		font-family: inherit;
	}
	.selected-geo:hover {
		border-color: var(--danger, #dc2626);
	}
	.selected-geo code {
		min-width: 0;
		overflow-wrap: anywhere;
		color: var(--text);
	}
	.selected-geo span {
		color: var(--muted-text);
		font-size: 0.9rem;
		line-height: 1;
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
	.field input {
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
	.field input:disabled {
		opacity: 0.7;
		cursor: not-allowed;
	}
	.rules-json-editor {
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 4px;
		width: 100%;
		box-sizing: border-box;
		height: calc(10 * 1.45em + 1rem);
	}

	.rules-json-editor:focus-within {
		border-color: var(--accent, #3b82f6);
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
		color: var(--color-accent-contrast, #ffffff);
		font-weight: 600;
	}
	.error {
		color: var(--danger, #dc2626);
		font-size: 0.85rem;
	}
	.parse-messages {
		padding: 0.55rem 0.65rem;
		border: 1px solid var(--border);
		border-radius: 0.45rem;
		background: var(--surface-1, rgba(255, 255, 255, 0.035));
		font-size: 0.82rem;
		line-height: 1.4;
		max-height: min(12rem, 32vh);
		overflow: auto;
		overflow-wrap: anywhere;
		word-break: break-word;
		min-width: 0;
	}

	.parse-messages-title {
		font-weight: 700;
		margin-bottom: 0.35rem;
	}

	.parse-messages ul {
		margin: 0;
		padding-left: 1.1rem;
		display: grid;
		gap: 0.22rem;
	}

	.parse-messages li {
		margin: 0;
	}

	.parse-messages-warning {
		border-color: var(--color-warning, #d97706);
		color: var(--color-warning, #d97706);
		background: rgba(217, 119, 6, 0.08);
	}
	.rules-editor {
		display: grid;
		grid-template-columns: auto 1fr;
		height: 16rem;
		min-height: 8rem;
		max-height: min(70vh, 36rem);
		resize: vertical;
		overflow: hidden;
		min-width: 0;
		align-items: stretch;
	}
	.line-numbers {
		margin: 0;
		padding: 0.5rem 0.45rem 0.5rem 0.55rem;
		border-right: 1px solid var(--border);
		background: var(--bg);
		color: var(--muted-text);
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
		line-height: 1.45;
		text-align: right;
		user-select: none;
		overflow: hidden;
		min-width: 2.25rem;
		white-space: pre;
		height: 100%;
		box-sizing: border-box;
	}
	.rules-editor-input {
		min-width: 0;
		min-height: 0;
		height: 100%;
		padding: 0.5rem 0.6rem;
		box-sizing: border-box;
	}

	.rules-editor-input :global(.shl-stack) {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
		line-height: 1.45;
	}

	.rules-json-editor :global(.shl-stack) {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
		line-height: 1.45;
	}

</style>
