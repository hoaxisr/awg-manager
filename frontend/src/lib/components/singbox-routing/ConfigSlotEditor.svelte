<!--
  Эксперт-редактор слота конфигурации sing-box.

  Два режима:
  - readonly: подсвеченный просмотр системного слота (генерируется
    автоматически, правки бессмысленны — продюсер перезапишет файл);
  - редактирование user-слота (90-user.json) через draft-пайплайн:
    Сохранить черновик (PUT) → Проверить (check, без записи) →
    Применить (apply: 422 с ошибками валидации) / Отменить черновик.

  Подсветка — textarea-underlay (SyntaxHighlightedTextarea + highlightJson),
  без новых зависимостей. Слева — гаттер с номерами строк, маркер на строке
  клиентской ошибки JSON.parse (debounce ~300 мс).
-->
<script lang="ts">
	import { Button, FormToggle, SyntaxHighlightedTextarea } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { highlightJson } from '$lib/utils/shareEditorHighlight';
	import { parseJsonErrorPosition, type JsonErrorPos } from '$lib/utils/jsonErrorPosition';
	import { stripAnsi } from '$lib/utils/ansi';
	import type {
		RouterStagingValidationError,
		RouterValidationErrorDTO,
	} from '$lib/types';

	interface Props {
		/** Имя файла слота (для подписей). */
		filename: string;
		/** Начальное содержимое (эффективное: pending-preferred). */
		content: string;
		/** true — просмотр системного слота, никаких действий кроме «Копировать». */
		readonly?: boolean;
		/** Включён ли слот (user-слот; переключатель рендерится в editable-режиме). */
		enabled?: boolean;
		/** Существует ли черновик на сервере (user-слот). */
		hasDraft?: boolean;
		/** Дёргается после apply/discard/enable — родитель обновляет список слотов. */
		onStateChanged?: () => void;
		/**
		 * Проброс dirty-состояния родителю: drawer спрашивает подтверждение
		 * перед закрытием/возвратом к списку, чтобы правки не пропали молча.
		 */
		onDirtyChange?: (dirty: boolean) => void;
	}

	let {
		filename,
		content,
		readonly = false,
		enabled = false,
		hasDraft = false,
		onStateChanged,
		onDirtyChange,
	}: Props = $props();

	// Пропсы фиксируются на маунте намеренно: родитель пересоздаёт редактор
	// через {#key} при смене слота/эпохи, дальше состояние живёт локально.
	// svelte-ignore state_referenced_locally
	let text = $state(content);
	/** Последнее содержимое, известное серверу (черновик или applied). */
	// svelte-ignore state_referenced_locally
	let serverText = $state(content);
	/** Черновик существует на сервере (сохранён, но не применён). */
	// svelte-ignore state_referenced_locally
	let draftSaved = $state(hasDraft);
	// svelte-ignore state_referenced_locally
	let slotEnabled = $state(enabled);
	/** База для best-effort предупреждения про tun-inbound. */
	// svelte-ignore state_referenced_locally
	const baselineText = content;

	let dirty = $derived(!readonly && text !== serverText);
	$effect(() => {
		onDirtyChange?.(dirty);
	});

	// ── клиентский JSON.parse с debounce ~300 мс ──────────────────────
	let parseError = $state<{ message: string; pos: JsonErrorPos | null } | null>(null);
	$effect(() => {
		const current = text;
		if (readonly) return;
		const timer = setTimeout(() => {
			if (current.trim().length === 0) {
				parseError = null;
				return;
			}
			try {
				JSON.parse(current);
				parseError = null;
			} catch (e) {
				const message = e instanceof Error ? e.message : String(e);
				parseError = { message, pos: parseJsonErrorPosition(message, current) };
			}
		}, 300);
		return () => clearTimeout(timer);
	});

	// ── гаттер ─────────────────────────────────────────────────────────
	let lineCount = $derived(Math.max(1, text.split(/\r?\n/).length));
	let errorLine = $derived(parseError?.pos?.line ?? 0);
	let textareaRef = $state<HTMLTextAreaElement | null>(null);
	let gutterEl = $state<HTMLDivElement | null>(null);
	let viewPreEl = $state<HTMLPreElement | null>(null);

	function syncGutterScroll(): void {
		if (!gutterEl) return;
		const src = readonly ? viewPreEl : textareaRef;
		if (!src) return;
		gutterEl.scrollTop = src.scrollTop;
	}

	// ── результаты серверной проверки / применения ────────────────────
	let checkBusy = $state(false);
	let saveBusy = $state(false);
	let applyBusy = $state(false);
	let discardBusy = $state(false);
	let enableBusy = $state(false);
	let busy = $derived(checkBusy || saveBusy || applyBusy || discardBusy || enableBusy);

	let serverErrors = $state<RouterValidationErrorDTO[] | null>(null);
	/** Advisory-предупреждения (severity=warning) — не блокируют применение. */
	let serverWarnings = $state<RouterValidationErrorDTO[] | null>(null);
	let sbCheckError = $state<string | null>(null);
	let checkOkShown = $state(false);

	function resetServerFeedback(): void {
		serverErrors = null;
		serverWarnings = null;
		sbCheckError = null;
		checkOkShown = false;
	}

	// Зелёный баннер «проверка пройдена» относится к проверенному тексту —
	// любая правка делает его недостоверным, гасим сразу.
	$effect(() => {
		void text;
		checkOkShown = false;
	});

	function tunCount(s: string): number {
		return (s.match(/"type"\s*:\s*"tun"/g) ?? []).length;
	}
	/** Best-effort: diff трогает tun-inbound → применение перезапустит sing-box. */
	let tunTouched = $derived(!readonly && tunCount(text) !== tunCount(baselineText));

	async function onCheck(): Promise<void> {
		if (checkBusy || parseError) return;
		checkBusy = true;
		resetServerFeedback();
		try {
			const res = await api.singboxUserConfigCheck(text);
			serverWarnings = res.warnings?.length ? res.warnings : null;
			if (res.ok) {
				checkOkShown = true;
			} else {
				serverErrors = res.errors ?? [];
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			checkBusy = false;
		}
	}

	async function saveDraft(silent = false): Promise<boolean> {
		try {
			await api.singboxUserConfigSave(text);
			serverText = text;
			draftSaved = true;
			if (!silent) notifications.success('Черновик сохранён');
			return true;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
			return false;
		}
	}

	async function onSaveDraft(): Promise<void> {
		if (saveBusy || parseError) return;
		saveBusy = true;
		resetServerFeedback();
		try {
			await saveDraft();
		} finally {
			saveBusy = false;
		}
	}

	async function onApply(): Promise<void> {
		if (applyBusy || parseError) return;
		applyBusy = true;
		resetServerFeedback();
		try {
			// Применяем ровно то, что в редакторе: несохранённые правки
			// сначала уходят в черновик.
			if (dirty || !draftSaved) {
				if (!(await saveDraft(true))) return;
			}
			const res = await api.singboxUserConfigApply();
			serverWarnings = res.warnings?.length ? res.warnings : null;
			draftSaved = false;
			slotEnabled = true; // apply включает припаркованный слот (после успешной валидации)
			notifications.success('Конфигурация применена');
			onStateChanged?.();
		} catch (e) {
			const err = e as { status?: number; body?: RouterStagingValidationError };
			if (err.status === 422 && err.body?.validation) {
				serverErrors = err.body.validation.errors;
			} else if (err.status === 422 && err.body?.sbCheck) {
				sbCheckError = stripAnsi(err.body.sbCheck);
			} else {
				notifications.error(e instanceof Error ? e.message : String(e));
			}
		} finally {
			applyBusy = false;
		}
	}

	async function onDiscard(): Promise<void> {
		if (discardBusy) return;
		discardBusy = true;
		resetServerFeedback();
		try {
			await api.singboxUserConfigDiscard();
			// Перечитываем эффективное содержимое (теперь active/disabled).
			const slot = await api.singboxConfigSlot('user');
			text = slot.content;
			serverText = slot.content;
			draftSaved = false;
			notifications.success('Черновик отменён');
			onStateChanged?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			discardBusy = false;
		}
	}

	async function onToggleEnabled(next: boolean): Promise<void> {
		if (enableBusy) return;
		enableBusy = true;
		try {
			await api.singboxUserConfigEnable(next);
			slotEnabled = next;
			notifications.success(next ? 'Слот включён' : 'Слот выключен');
			onStateChanged?.();
		} catch (e) {
			slotEnabled = !next; // откат оптимистичного переключения
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			enableBusy = false;
		}
	}

	async function onCopy(): Promise<void> {
		const ok = await copyToClipboard(text);
		if (ok) notifications.success('Скопировано в буфер обмена');
		else notifications.error('Не удалось скопировать');
	}

	// Leave-guard: несохранённые правки не должны молча пропасть.
	function onBeforeUnload(e: BeforeUnloadEvent): void {
		if (!dirty) return;
		e.preventDefault();
	}

	let statusLabel = $derived(
		readonly
			? ''
			: dirty
				? 'изменено, не сохранено'
				: draftSaved
					? 'черновик сохранён, не применён'
					: 'применено',
	);
</script>

<svelte:window onbeforeunload={onBeforeUnload} />

<div class="slot-editor">
	{#if !readonly}
		<div class="hint">
			Слот дополняет конфиг: свои outbounds/inbounds/правила добавляются последними.
			Скаляры dns/route (final, strategy) задаются в настройках роутера и отсюда не
			переопределяются. Теги не должны совпадать с существующими.
		</div>
	{/if}

	<div class="editor-frame" class:readonly>
		<div class="gutter" bind:this={gutterEl} aria-hidden="true">
			<div class="gutter-inner">
				{#each Array.from({ length: lineCount }, (_, i) => i + 1) as n (n)}
					<div class="ln" class:err={n === errorLine}>{n}</div>
				{/each}
			</div>
		</div>
		<div class="editor-input">
			{#if readonly}
				<!-- Просмотр: обычный pre с той же подсветкой, скроллится сам. -->
				<pre class="view-pre" bind:this={viewPreEl} onscroll={syncGutterScroll}>{@html highlightJson(text)}</pre>
			{:else}
				<SyntaxHighlightedTextarea
					bind:value={text}
					bind:textareaRef
					highlight={highlightJson}
					indentMode="json"
					wrap="pre"
					placeholder={'{ }'}
					onscroll={syncGutterScroll}
				/>
			{/if}
		</div>
	</div>

	{#if parseError}
		<div class="feedback error">
			<div class="feedback-title">
				Некорректный JSON{#if parseError.pos}
					&nbsp;— строка {parseError.pos.line}, колонка {parseError.pos.column}{/if}
			</div>
			<div class="feedback-body">{parseError.message}</div>
		</div>
	{/if}

	{#if serverErrors}
		<div class="feedback error">
			<div class="feedback-title">Проверка не пройдена</div>
			<ul class="error-list">
				<!-- Ключ с индексом: повторные duplicate-* ошибки совпадают по
				     slot/kind/tag (пустой inRule) и без индекса роняют keyed-each. -->
				{#each serverErrors as e, i (`${i}-${e.kind}`)}
					<li>
						<strong>{e.inRule || e.slot}</strong>: {e.message}{#if e.tag}
							&nbsp;({e.tag}){/if}
						<span class="err-kind">[{e.kind}]</span>
					</li>
				{/each}
			</ul>
		</div>
	{/if}

	{#if serverWarnings}
		<div class="feedback warn">
			<div class="feedback-title">Предупреждения — применение возможно</div>
			<ul class="error-list">
				{#each serverWarnings as w, i (`${i}-${w.kind}`)}
					<li>
						<strong>{w.inRule || w.slot}</strong>: {w.message}{#if w.tag}
							&nbsp;({w.tag}){/if}
						<span class="err-kind">[{w.kind}]</span>
					</li>
				{/each}
			</ul>
		</div>
	{/if}

	{#if sbCheckError}
		<div class="feedback error">
			<div class="feedback-title">sing-box check</div>
			<pre class="sb-check">{sbCheckError}</pre>
		</div>
	{/if}

	{#if checkOkShown}
		<div class="feedback ok">Проверка пройдена — конфиг совместим с активными слотами.</div>
	{/if}

	{#if !readonly && tunTouched}
		<div class="feedback warn">
			Изменение tun-inbound вызовет полный перезапуск sing-box (SIGHUP его не подхватывает).
		</div>
	{/if}

	<div class="toolbar">
		{#if readonly}
			<span class="readonly-note">Генерируется автоматически — правки перезапишет продюсер слота.</span>
			<div class="spacer"></div>
			<Button variant="secondary" size="sm" onclick={onCopy}>Копировать</Button>
		{:else}
			<span class="status" class:dirty>{statusLabel}</span>
			<div class="spacer"></div>
			<FormToggle
				label={slotEnabled ? 'Включён' : 'Выключен'}
				bind:checked={slotEnabled}
				disabled={busy}
				onchange={(v: boolean) => void onToggleEnabled(v)}
			/>
			<Button
				variant="ghost"
				size="sm"
				disabled={busy || !draftSaved}
				onclick={onDiscard}
			>
				{discardBusy ? 'Отменяю…' : 'Отменить черновик'}
			</Button>
			<Button
				variant="secondary"
				size="sm"
				disabled={busy || !!parseError}
				onclick={onCheck}
			>
				{checkBusy ? 'Проверяю…' : 'Проверить'}
			</Button>
			<Button
				variant="secondary"
				size="sm"
				disabled={busy || !!parseError || !dirty}
				onclick={onSaveDraft}
			>
				{saveBusy ? 'Сохраняю…' : 'Сохранить черновик'}
			</Button>
			<Button
				variant="primary"
				size="sm"
				disabled={busy || !!parseError || (!dirty && !draftSaved)}
				onclick={onApply}
			>
				{applyBusy ? 'Применяю…' : 'Применить'}
			</Button>
		{/if}
	</div>
	<div class="filename">{filename}</div>
</div>

<style>
	.slot-editor {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
		min-height: 0;
		flex: 1;
	}

	.hint {
		padding: 0.5rem 0.65rem;
		background: var(--color-bg-tertiary);
		border-radius: var(--radius-sm);
		font-size: 0.8rem;
		line-height: 1.4;
		color: var(--color-text-secondary);
	}

	/* Рамка редактора: гаттер + слой подсветки. Общие моноширинные метрики
	   заданы на .editor-frame и наследуются ОБОИМИ слоями (это критично для
	   совпадения underlay и textarea). Перенос выключен (wrap="pre"). */
	.editor-frame {
		display: grid;
		grid-template-columns: auto 1fr;
		flex: 1;
		min-height: 16rem;
		max-height: 100%;
		overflow: hidden;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace;
		font-size: 0.8rem;
		line-height: 1.45;
	}
	.editor-frame:focus-within {
		border-color: var(--color-accent);
	}

	.gutter {
		overflow: hidden;
		border-right: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg-primary) 85%, var(--color-border));
		user-select: none;
		min-width: 2.4rem;
	}
	.gutter-inner {
		padding: 0.5rem 0;
	}
	.ln {
		padding: 0 0.5rem 0 0.6rem;
		text-align: right;
		color: var(--color-text-muted);
		white-space: pre;
	}
	.ln.err {
		color: var(--color-error);
		font-weight: 700;
		background: color-mix(in srgb, var(--color-error) 18%, transparent);
	}

	.editor-input {
		min-width: 0;
		min-height: 0;
		padding: 0.5rem 0.6rem;
		box-sizing: border-box;
	}
	.editor-input :global(.shl-stack) {
		height: 100%;
	}

	.view-pre {
		margin: 0;
		padding: 0;
		height: 100%;
		overflow: auto;
		font: inherit;
		white-space: pre;
		color: var(--color-text-primary);
	}
	/* Токены подсветки для readonly-pre (editable-слой стилизует их сам). */
	.view-pre :global(.hl-json-key) {
		color: var(--hl-json-key, #0284c7);
	}
	.view-pre :global(.hl-json-str) {
		color: var(--hl-json-str, var(--color-text-primary));
	}
	.view-pre :global(.hl-json-num) {
		color: var(--hl-json-num, #ea580c);
	}
	.view-pre :global(.hl-json-lit) {
		color: var(--hl-json-lit, #9333ea);
	}
	.view-pre :global(.hl-json-punct) {
		color: var(--hl-json-punct, var(--color-text-muted));
	}

	.feedback {
		padding: 0.5rem 0.65rem;
		border-radius: var(--radius-sm);
		font-size: 0.82rem;
		line-height: 1.4;
	}
	.feedback.error {
		border: 1px solid var(--color-error);
		background: color-mix(in srgb, var(--color-error) 10%, transparent);
	}
	.feedback.ok {
		border: 1px solid var(--color-success, #16a34a);
		background: color-mix(in srgb, var(--color-success, #16a34a) 10%, transparent);
	}
	.feedback.warn {
		border: 1px solid var(--color-warning);
		background: color-mix(in srgb, var(--color-warning) 10%, transparent);
	}
	.feedback-title {
		font-weight: 600;
		margin-bottom: 0.25rem;
	}
	.feedback-body {
		color: var(--color-text-secondary);
		word-break: break-word;
	}
	.error-list {
		margin: 0;
		padding-left: 1.25rem;
	}
	.err-kind {
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}
	.sb-check {
		margin: 0;
		font-size: 0.75rem;
		white-space: pre-wrap;
		overflow-x: auto;
	}

	.toolbar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.spacer {
		flex: 1;
	}
	.status {
		font-size: 0.78rem;
		color: var(--color-text-muted);
	}
	.status.dirty {
		color: var(--color-warning);
		font-weight: 600;
	}
	.readonly-note {
		font-size: 0.78rem;
		color: var(--color-text-muted);
	}
	.filename {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		font-family: ui-monospace, monospace;
	}
</style>
