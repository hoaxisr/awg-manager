<script lang="ts">
	import { m, uiText, type UiText } from '$lib/i18n';
	import { goto } from '$app/navigation';
	import { Modal, Button, Dropdown } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { linkImportErrorText } from '$lib/utils/linkImportError';
	import { isTrustTunnelMultiAddress } from '$lib/utils/trustTunnelImport';
	import { singboxStatus, singboxTunnels } from '$lib/stores/singbox';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { usageLevel } from '$lib/stores/settings';
	import {
		DEFAULT_SUBSCRIPTION_URLTEST,
		type SubscriptionMode,
		type SubscriptionPreviewMember,
	} from '$lib/types';
	import { Check, LayoutGrid, Link, Globe, Waypoints, FileText } from 'lucide-svelte';
	import HeadersTextarea from './HeadersTextarea.svelte';
	import HappKeysModal from './HappKeysModal.svelte';
	import ShareLinksTextarea from './ShareLinksTextarea.svelte';
	import SubscriptionImportPreview from './SubscriptionImportPreview.svelte';
	import RoutingImportDropZone from '$lib/components/routing/RoutingImportDropZone.svelte';
	import { FilePickerModal } from '$lib/components/system/files';
	import {
		DEFAULT_PRESET,
		parseHeadersText,
	} from './headersParser';
	import {
		appendImportedFileText,
		mergePastedShareList,
		normalizeSpaceSeparatedShareLinks,
	} from '$lib/utils/shareLinkListInput';

	type WizardKind = 'single' | 'inline' | 'url' | 'file';

	interface Props {
		open: boolean;
		/** Preselect a step-2 form. When unset (or 'choose') the wizard
		 * opens on step 1 (kind cards). Callers from contextual
		 * "+ Add" buttons usually pass a preselect; emptystate cards
		 * can also pass a preselect to skip step 1. */
		preselect?: WizardKind | 'choose';
		onclose?: () => void;
		/** Fourth choose-card: close wizard and open AWG3 import modal. */
		onAwg3?: () => void;
	}

	let { open = $bindable(false), preselect = 'choose', onclose, onAwg3 }: Props = $props();

	let kind = $state<WizardKind | 'choose'>('choose');
	let submitting = $state(false);
	let error = $state<UiText>('');

	// "Один сервер" state — paste of N share-links, each becomes its
	// own sing-box tunnel via /singbox/import-links.
	let singleLinks = $state('');
	let singleResult = $state<{ imported: number; errors: string[] } | null>(null);

	// "Группа серверов" / "Подписка" — shared subscription create state.
	let label = $state('');
	let url = $state('');
	let filePath = $state('');
	let showFilePicker = $state(false);
	let inlineText = $state('');
	let headersText = $state(DEFAULT_PRESET);
	let refreshHoursStr = $state('24');
	let refreshHours = $state(24);
	let enabled = $state(true);
	let mode = $state<SubscriptionMode>('selector');
	let utUrl = $state(DEFAULT_SUBSCRIPTION_URLTEST.url);
	let utIntervalSec = $state(DEFAULT_SUBSCRIPTION_URLTEST.intervalSec);
	let utToleranceMs = $state(DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs);

	// URL-subscription import has a two-step flow: fill the URL form, then pick
	// which servers to import. Unchecked members are excluded at create time.
	let urlStep = $state<'form' | 'preview'>('form');
	let previewMembers = $state<SubscriptionPreviewMember[]>([]);
	let excludedKeys = $state<Set<string>>(new Set());
	let previewing = $state(false);

	$effect(() => {
		refreshHours = parseInt(refreshHoursStr, 10) || 0;
	});

	const refreshOptions = $derived([
		{ value: '0', label: m.subscriptions_refresh_manual() },
		{ value: '1', label: m.subscriptions_refresh_hourly() },
		{ value: '6', label: m.subscriptions_refresh_6h() },
		{ value: '12', label: m.subscriptions_refresh_12h() },
		{ value: '24', label: m.subscriptions_refresh_daily() },
		{ value: '168', label: m.subscriptions_refresh_weekly() },
	]);

	const singboxInstalled = $derived($singboxStatus.data?.installed ?? false);
	// URL- и файловая подписки делят двухшаговый поток «форма → выбор серверов».
	const hasPreviewStep = $derived(kind === 'url' || kind === 'file');
	// Compare every form field against its reset() default. The previous
	// `kind !== 'choose'` heuristic mis-fired the moment the user picked
	// a step (or arrived with a preselect), claiming dirty without any
	// input. Now dirty reflects actual edits.
	const isDirty = $derived.by(() => {
		if (kind === 'choose') return false;
		if (kind === 'single') return singleLinks.trim() !== '';
		// 'inline', 'url' and 'file' share the subscription form below.
		return (
			label.trim() !== '' ||
			url.trim() !== '' ||
			filePath.trim() !== '' ||
			inlineText.trim() !== '' ||
			headersText !== DEFAULT_PRESET ||
			refreshHoursStr !== '24' ||
			enabled !== true ||
			mode !== 'selector' ||
			utUrl !== DEFAULT_SUBSCRIPTION_URLTEST.url ||
			utIntervalSec !== DEFAULT_SUBSCRIPTION_URLTEST.intervalSec ||
			utToleranceMs !== DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs
		);
	});

	$effect(() => {
		if (open) {
			kind = preselect;
		}
	});

	let detectingHeaders = $state(false);
	let detectedNotice = $state<UiText>('');
	let detectStatus = $state<'ok' | 'keys' | 'error'>('ok');
	let showHappKeysModal = $state(false);
	let detectTimer: ReturnType<typeof setTimeout> | null = null;
	// Поколение запроса: ответ детекта, устаревший к моменту прихода, не должен
	// переписать headersText от нового URL и не должен гасить чужой индикатор.
	let detectSeq = 0;
	let lastDetectedUrl = '';
	let lastNormalizedUrl = '';

	function triggerDetectHeaders(targetUrl: string, immediate = false): void {
		if (detectTimer) clearTimeout(detectTimer);
		const raw = targetUrl.trim();
		// Нормализацию (снятие обёрток happ:// / clash:// и расшифровку
		// happ://crypt) делает сервер и возвращает в normalizedUrl — здесь
		// только грубый отсев того, что ещё не похоже на ссылку.
		if (!raw.includes('://')) {
			detectSeq++;
			detectingHeaders = false;
			detectedNotice = '';
			detectStatus = 'ok';
			lastDetectedUrl = '';
			lastNormalizedUrl = '';
			return;
		}
		// onpaste и следующий за ним onblur дают один и тот же URL — вторая
		// серия проб роутеру не нужна.
		if (raw === lastDetectedUrl || raw === lastNormalizedUrl) return;
		detectedNotice = '';
		detectStatus = 'ok';

		const runDetect = async () => {
			const seq = ++detectSeq;
			lastDetectedUrl = raw;
			detectingHeaders = true;
			try {
				const res = await api.detectSubscriptionHeaders(raw, parseHeadersText(headersText));
				if (seq !== detectSeq) return;
				if (res?.normalizedUrl) {
					lastNormalizedUrl = res.normalizedUrl;
					url = res.normalizedUrl;
				}
				if (res && res.serverCount > 0) {
					headersText = res.headersText;
					const { label, serverCount: count } = res;
					if (res.isEncrypted && res.decryptedUrl) {
						const decryptedUrl = res.decryptedUrl;
						detectedNotice = () => m.subscriptions_wizard_decrypted_full({ url: decryptedUrl, label, count });
					} else {
						detectedNotice = () => m.subscriptions_wizard_recognized({ label, count });
					}
				} else if (res && res.isEncrypted && res.decryptedUrl) {
					const decryptedUrl = res.decryptedUrl;
					detectedNotice = () => m.subscriptions_wizard_decrypted({ url: decryptedUrl });
				} else if (res && res.isEncrypted && !res.decryptedUrl) {
					detectStatus = 'keys';
					detectedNotice = () => m.subscriptions_wizard_happ_encrypted();
				}
			} catch (e) {
				if (seq !== detectSeq) return;
				// Повторить детект по тому же URL после ошибки должно быть можно.
				lastDetectedUrl = '';
				detectStatus = 'error';
				detectedNotice =
					e instanceof Error ? e.message : () => m.subscriptions_wizard_detect_failed();
			} finally {
				if (seq === detectSeq) {
					detectingHeaders = false;
				}
			}
		};

		if (immediate) {
			void runDetect();
		} else {
			detectTimer = setTimeout(runDetect, 250);
		}
	}

	function reset(): void {
		kind = 'choose';
		singleLinks = '';
		singleResult = null;
		label = '';
		url = '';
		filePath = '';
		inlineText = '';
		headersText = DEFAULT_PRESET;
		refreshHoursStr = '24';
		refreshHours = 24;
		enabled = true;
		mode = 'selector';
		utUrl = DEFAULT_SUBSCRIPTION_URLTEST.url;
		utIntervalSec = DEFAULT_SUBSCRIPTION_URLTEST.intervalSec;
		utToleranceMs = DEFAULT_SUBSCRIPTION_URLTEST.toleranceMs;
		urlStep = 'form';
		previewMembers = [];
		excludedKeys = new Set();
		previewing = false;
		detectingHeaders = false;
		detectedNotice = '';
		detectStatus = 'ok';
		lastDetectedUrl = '';
		lastNormalizedUrl = '';
		error = '';
	}

	function close(): void {
		if (submitting) return;
		open = false;
		reset();
		onclose?.();
	}

	function pickAwg3(): void {
		if (submitting || !onAwg3) return;
		open = false;
		reset();
		onAwg3();
	}

	function backToChoose(): void {
		if (submitting) return;
		kind = 'choose';
		error = '';
	}

	function onShareListPaste(
		e: ClipboardEvent & { currentTarget: HTMLTextAreaElement },
		get: () => string,
		set: (v: string) => void,
	): void {
		const data = e.clipboardData?.getData('text/plain');
		if (data == null) return;
		const normalized = normalizeSpaceSeparatedShareLinks(data);
		if (normalized === data) return;
		e.preventDefault();
		const ta = e.currentTarget;
		const { next, caret } = mergePastedShareList(
			get(),
			ta.selectionStart ?? 0,
			ta.selectionEnd ?? 0,
			data,
		);
		set(next);
		queueMicrotask(() => {
			ta.selectionStart = ta.selectionEnd = caret;
		});
	}

	// Загрузка файла в textarea импорта: share-link'и, Clash YAML, sing-box
	// JSON или mieru JSON (экспорт панелей, формат mieru apply config). Пустое
	// поле заменяем содержимым файла, непустое — дописываем с новой строки.
	async function onImportFile(file: File, get: () => string, set: (v: string) => void): Promise<void> {
		// Кап тела запроса на бэкенде — 1 МБ (http.MaxBytesReader): больший
		// файл упал бы только на submit с невнятным 413.
		if (file.size > 1 << 20) {
			error = () => m.subscriptions_wizard_file_too_big({ name: file.name });
			return;
		}
		try {
			const text = await file.text();
			if (!text.trim()) {
				error = () => m.subscriptions_wizard_file_empty({ name: file.name });
				return;
			}
			const merged = appendImportedFileText(get(), text);
			if (merged.rejected) {
				error = () => m.subscriptions_wizard_config_whole_only();
				return;
			}
			set(merged.text);
			error = '';
		} catch {
			error = () => m.subscriptions_wizard_file_read_failed({ name: file.name });
		}
	}

	// «Один сервер» понимает share-ссылки, mieru JSON и TrustTunnel TOML;
	// Clash YAML и sing-box JSON принимает лишь ветка «Группа серверов».
	const IMPORT_FILE_ACCEPT_SINGLE = '.json,.txt,.toml';
	const IMPORT_FILE_ACCEPT = '.json,.txt,.yaml,.yml,.toml';

	const titleByKind = $derived<Record<WizardKind | 'choose', string>>({
		choose: m.common_add(),
		single: m.tunnels_create_single_title(),
		inline: m.tunnels_create_group_title(),
		url: m.tunnels_create_sub_title(),
		file: m.subscriptions_wizard_kind_file(),
	});

	async function submitSingle(): Promise<void> {
		singleLinks = normalizeSpaceSeparatedShareLinks(singleLinks);
		if (!singleLinks.trim() || submitting) return;
		submitting = true;
		error = '';
		singleResult = null;
		try {
			const res = await api.singboxImportLinks(singleLinks);
			singboxTunnels.applyMutationResponse(res.tunnels);
			singleResult = {
				imported: res.imported?.length ?? 0,
				errors: (res.errors ?? []).map((e) => linkImportErrorText(e.error)),
			};
			if ((res.imported?.length ?? 0) > 0) {
				open = false;
				reset();
				goto('/?tab=singbox');
			}
		} catch (e) {
			const n = isTrustTunnelMultiAddress(e);
			if (n > 0) {
				inlineText = singleLinks;
				singleLinks = '';
				kind = 'inline';
				error = () => m.subscriptions_wizard_trusttunnel_multi({ count: n });
				return;
			}
			error = e instanceof Error ? e.message : () => m.subscriptions_wizard_import_failed();
		} finally {
			submitting = false;
		}
	}

	async function fetchPreview(): Promise<void> {
		const isFile = kind === 'file';
		if (previewing || (isFile ? !filePath.trim() : !url.trim())) {
			error = isFile ? () => m.subscriptions_wizard_enter_path() : () => m.subscriptions_wizard_enter_url();
			return;
		}
		previewing = true;
		error = '';
		try {
			// Превью файла читает тот же путь, что уйдёт в создание, — потому
			// и здесь trim(), иначе превью и submit разошлись бы по строке.
			const members = await api.previewSubscription(
				isFile
					? { path: filePath.trim(), headers: [] }
					: { url, headers: parseHeadersText(headersText) },
			);
			// Дедуп по key обязателен: список рендерится keyed each'ем по
			// member.key, и дубликат ключа роняет рендер (each_key_duplicate) —
			// модалка замирает на «Загрузка...» (issue #428). Бэкенд уже
			// дедуплицирует, это страховка от старых бэкендов и иных источников.
			const seen = new Set<string>();
			previewMembers = (members ?? []).filter((member) => {
				if (seen.has(member.key)) return false;
				seen.add(member.key);
				return true;
			});
			excludedKeys = new Set();
			urlStep = 'preview';
		} catch (e) {
			error = e instanceof Error ? e.message : () => m.subscriptions_wizard_preview_failed();
		} finally {
			previewing = false;
		}
	}

	function toggleExcluded(key: string): void {
		const next = new Set(excludedKeys);
		if (next.has(key)) next.delete(key);
		else next.add(key);
		excludedKeys = next;
	}

	function selectAllMembers(): void {
		excludedKeys = new Set();
	}

	function selectNoneMembers(): void {
		excludedKeys = new Set(previewMembers.map((member) => member.key));
	}

	async function submitSubscription(): Promise<void> {
		if (submitting) return;
		const isInline = kind === 'inline';
		const isFile = kind === 'file';
		if (isInline) {
			inlineText = normalizeSpaceSeparatedShareLinks(inlineText);
		}
		if (isInline && !inlineText.trim()) {
			error = () => m.subscriptions_wizard_paste_link();
			return;
		}
		if (isFile && !filePath.trim()) {
			error = () => m.subscriptions_wizard_enter_path();
			return;
		}
		if (!isInline && !isFile && !url.trim()) {
			error = () => m.subscriptions_wizard_enter_url();
			return;
		}
		submitting = true;
		error = '';
		try {
			const sub = await api.createSubscription({
				label,
				url: kind === 'url' ? url : undefined,
				path: isFile ? filePath.trim() : undefined,
				inline: isInline ? inlineText : undefined,
				headers: kind === 'url' ? parseHeadersText(headersText) : [],
				refreshHours: kind === 'url' ? refreshHours : 0,
				enabled,
				mode,
				urlTest:
					mode === 'urltest'
						? { url: utUrl, intervalSec: utIntervalSec, toleranceMs: utToleranceMs }
						: undefined,
				excludedKeys: isInline ? undefined : [...excludedKeys],
			});
			// Keep tunnels tab + sb-router wizard in sync: list outbounds are not polled.
			await subscriptionsStore.refetch();
			try {
				singboxRouter.applyOutbounds(await api.singboxRouterListOutbounds());
			} catch {
				/* routing UI will refresh on next loadAll */
			}
			open = false;
			reset();
			goto(`/subscriptions/${sub.id}`);
		} catch (e) {
			error = e instanceof Error ? e.message : () => m.subscriptions_wizard_create_failed();
		} finally {
			submitting = false;
		}
	}
</script>

<Modal
	{open}
	title={titleByKind[kind]}
	size={kind === 'choose' ? 'wide' : 'lg'}
	onclose={close}
	hasUnsavedChanges={() => isDirty}
>
	{#if kind === 'choose'}
		<p class="lead">{m.subscriptions_wizard_lead()}</p>
		<div class="kind-grid">
			<button type="button" class="kind-card" onclick={() => (kind = 'single')}>
				<Link size={28} strokeWidth={1.6} style="color: var(--color-primary, #3b82f6)" aria-hidden="true" />
				<div class="kind-title">{m.tunnels_create_single_title()}</div>
				<div class="kind-desc">
					{m.subscriptions_wizard_single_desc()}
				</div>
			</button>
			<button type="button" class="kind-card" onclick={() => (kind = 'inline')}>
				<LayoutGrid size={28} strokeWidth={1.6} style="color: var(--color-primary, #3b82f6)" aria-hidden="true" />
				<div class="kind-title">{m.tunnels_create_group_title()}</div>
				<div class="kind-desc">
					{m.subscriptions_wizard_group_desc()}
				</div>
			</button>
			<button type="button" class="kind-card" onclick={() => (kind = 'url')}>
				<Globe size={28} strokeWidth={1.6} style="color: var(--color-primary, #3b82f6)" aria-hidden="true" />
				<div class="kind-title">{m.tunnels_create_sub_title()}</div>
				<div class="kind-desc">
					{m.subscriptions_wizard_url_desc()}
				</div>
			</button>
			<button type="button" class="kind-card" onclick={() => (kind = 'file')}>
				<FileText size={28} strokeWidth={1.6} style="color: var(--color-primary, #3b82f6)" aria-hidden="true" />
				<div class="kind-title">{m.subscriptions_wizard_kind_file()}</div>
				<div class="kind-desc">
					{m.subscriptions_wizard_file_desc()}
				</div>
			</button>
			{#if onAwg3}
				<button type="button" class="kind-card" onclick={pickAwg3}>
					<Waypoints size={28} strokeWidth={1.6} style="color: var(--color-primary, #3b82f6)" aria-hidden="true" />
					<div class="kind-title">AWG3 Endpoint</div>
					<div class="kind-desc">
						{m.subscriptions_wizard_awg3_desc()}
					</div>
				</button>
			{/if}
		</div>
	{:else if kind === 'single'}
		<form
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				void submitSingle();
			}}
		>
			<p class="lead">
				{m.subscriptions_wizard_single_lead_1()} <code>vless://</code>, <code>hy2://</code>,
				<code>trojan://</code>, <code>ss://</code>, <code>hysteria2://</code>,
				<code>mieru://</code>, <code>mierus://</code>,
				<code>naive+http://</code>, <code>naive+https://</code>,
				<code>tt://</code> {m.subscriptions_wizard_single_lead_2()} (<code>…?d=</code>),
				{m.subscriptions_wizard_single_lead_3()}
				<code>mieru apply config</code>) {m.subscriptions_wizard_single_lead_4()}
			</p>
			{#if !singboxInstalled}
				<div class="warn">
					{m.subscriptions_wizard_no_singbox()}
				</div>
			{/if}
			<ShareLinksTextarea
				bind:value={singleLinks}
				placeholder={`vless://uuid@host:443?...#Germany\nhysteria2://pass@host:8443#Finland\nmierus://user:pass@host?profile=default&port=443&protocol=TCP\ntt://?AQ92cG4uZXhh...`}
				rows={6}
				disabled={!singboxInstalled || submitting}
				onpaste={(e) => onShareListPaste(e, () => singleLinks, (v) => (singleLinks = v))}
			/>
			<RoutingImportDropZone
				dropTitle={m.subscriptions_wizard_drop_single()}
				accept={IMPORT_FILE_ACCEPT_SINGLE}
				onfile={(f) => void onImportFile(f, () => singleLinks, (v) => (singleLinks = v))}
			/>
			{#if error}<div class="err">{uiText(error)}</div>{/if}
			{#if singleResult && singleResult.errors.length > 0}
				<div class="err">
					<div>{m.subscriptions_wizard_import_result({ imported: singleResult.imported, errors: singleResult.errors.length })}</div>
					<ul class="err-list">
						{#each singleResult.errors as e}<li>{e}</li>{/each}
					</ul>
				</div>
			{/if}
		</form>
	{:else if hasPreviewStep && urlStep === 'preview'}
		<div class="steps" aria-hidden="true">
			<span class="step done">{kind === 'file' ? m.subscriptions_wizard_step_file() : m.subscriptions_wizard_step_url()}</span>
			<span class="step-sep">›</span>
			<span class="step current">{m.subscriptions_wizard_step_pick()}</span>
			<span class="step-sep">›</span>
			<span class="step">{m.common_done()}</span>
		</div>
		<SubscriptionImportPreview
			members={previewMembers}
			{excludedKeys}
			ontoggle={toggleExcluded}
			onselectAll={selectAllMembers}
			onselectNone={selectNoneMembers}
		/>
		{#if error}<div class="err">{uiText(error)}</div>{/if}
	{:else}
		<form
			class="form"
			onsubmit={(e) => {
				e.preventDefault();
				if (hasPreviewStep) void fetchPreview();
				else void submitSubscription();
			}}
		>
			{#if hasPreviewStep}
				<div class="steps" aria-hidden="true">
					<span class="step current">{kind === 'file' ? m.subscriptions_wizard_step_file() : m.subscriptions_wizard_step_url()}</span>
					<span class="step-sep">›</span>
					<span class="step">{m.subscriptions_wizard_step_pick()}</span>
					<span class="step-sep">›</span>
					<span class="step">{m.common_done()}</span>
				</div>
			{/if}
			<label class="row">
				<span class="lbl">{m.subscriptions_form_name()}</span>
				<input class="inp" type="text" bind:value={label} placeholder="Provider X" required />
			</label>

			{#if kind === 'url'}
				<label class="row">
					<span class="lbl">{m.subscriptions_wizard_url_label()}</span>
					<input
						class="inp"
						type="url"
						bind:value={url}
						onpaste={() => setTimeout(() => triggerDetectHeaders(url, true), 0)}
						onblur={() => triggerDetectHeaders(url, true)}
						oninput={() => triggerDetectHeaders(url)}
						placeholder={m.subscriptions_wizard_url_placeholder()}
					/>
					{#if detectingHeaders}
						<div class="detect-badge detect-loading">
							<span>{m.subscriptions_wizard_detecting()}</span>
						</div>
					{:else if detectedNotice}
						<div
							class="detect-badge"
							class:detect-warning={detectStatus !== 'ok'}
							class:detect-success={detectStatus === 'ok'}
						>
							<span>{uiText(detectedNotice)}</span>
							{#if detectStatus === 'keys'}
								<Button
									size="sm"
									variant="secondary"
									onclick={() => (showHappKeysModal = true)}
								>
									{m.subscriptions_wizard_enter_keys()}
								</Button>
							{/if}
						</div>
					{:else}
						<span class="hint">
							{m.subscriptions_wizard_url_hint()}
						</span>
					{/if}
				</label>
				<div class="row">
					<HeadersTextarea bind:value={headersText} />
				</div>
				<div class="row">
					<Dropdown
						label={m.subscriptions_card_detail_auto_refresh()}
						bind:value={refreshHoursStr}
						options={refreshOptions}
						fullWidth
					/>
				</div>
			{:else if kind === 'file'}
				<!-- «Выбрать…» стоит в одной строке с полем: снаружи .row кнопка
				     отрывалась от поля и читалась как отдельный раздел формы.
				     Строка — <div> с <label for>, потому что <button> внутри
				     <label> нарушает его модель содержимого (labelable element). -->
				<div class="row">
					<label class="lbl" for="sub-file-path">{m.subscriptions_wizard_path_label()}</label>
					<div class="path-line">
						<input
							id="sub-file-path"
							class="inp mono"
							type="text"
							bind:value={filePath}
							placeholder="/opt/etc/awg-manager/sub.txt"
							autocomplete="off"
							spellcheck="false"
						/>
						{#if $usageLevel === 'expert'}
							<Button variant="secondary" size="sm" onclick={() => (showFilePicker = true)}>
								{m.subscriptions_wizard_pick_file()}
							</Button>
						{/if}
					</div>
					<span class="hint">
						{m.subscriptions_wizard_path_hint()}
					</span>
				</div>
			{:else}
				<label class="row">
					<span class="lbl">{m.subscriptions_wizard_links_label()}</span>
					<ShareLinksTextarea
						bind:value={inlineText}
						placeholder={`vless://...\ntrojan://...\nhysteria2://...\nnaive+https://\nss://...\nmieru://...\ntt://...`}
						rows={6}
						onpaste={(e) => onShareListPaste(e, () => inlineText, (v) => (inlineText = v))}
					/>
					<span class="hint">
						{m.subscriptions_wizard_inline_hint()}
					</span>
				</label>
				<RoutingImportDropZone
					dropTitle={m.subscriptions_wizard_drop_multi()}
					accept={IMPORT_FILE_ACCEPT}
					onfile={(f) => void onImportFile(f, () => inlineText, (v) => (inlineText = v))}
				/>
			{/if}

			<div class="row">
				<span class="lbl">{m.subscriptions_form_mode()}</span>
				<div class="mode-grid" role="radiogroup" aria-label={m.subscriptions_form_mode()}>
					<button
						type="button"
						role="radio"
						aria-checked={mode === 'selector'}
						class="mode-card"
						class:selected={mode === 'selector'}
						onclick={() => (mode = 'selector')}
					>
						<div class="mode-title">{m.subscriptions_form_mode_selector_title()}</div>
						<div class="mode-desc">
							{m.subscriptions_form_mode_selector_desc()}
						</div>
						{#if mode === 'selector'}
							<span class="mode-check" aria-hidden="true">
								<Check size={12} strokeWidth={3} aria-hidden="true" />
							</span>
						{/if}
					</button>
					<button
						type="button"
						role="radio"
						aria-checked={mode === 'urltest'}
						class="mode-card"
						class:selected={mode === 'urltest'}
						onclick={() => (mode = 'urltest')}
					>
						<div class="mode-title">{m.subscriptions_form_mode_urltest_title()}</div>
						<div class="mode-desc">
							{m.subscriptions_form_mode_urltest_desc()}
						</div>
						{#if mode === 'urltest'}
							<span class="mode-check" aria-hidden="true">
								<Check size={12} strokeWidth={3} aria-hidden="true" />
							</span>
						{/if}
					</button>
				</div>
			</div>

			{#if mode === 'urltest'}
				<div class="urltest-block">
					<label class="row">
						<span class="lbl">{m.subscriptions_form_check_url()}</span>
						<input
							class="inp"
							type="url"
							bind:value={utUrl}
							placeholder={DEFAULT_SUBSCRIPTION_URLTEST.url}
						/>
					</label>
					<div class="row two-col">
						<label class="col">
							<span class="lbl">{m.subscriptions_form_interval()}</span>
							<input class="inp" type="number" min="10" max="3600" bind:value={utIntervalSec} />
						</label>
						<label class="col">
							<span class="lbl">{m.subscriptions_form_tolerance()}</span>
							<input class="inp" type="number" min="0" max="2000" bind:value={utToleranceMs} />
						</label>
					</div>
				</div>
			{/if}

			<label class="row chk">
				<input type="checkbox" bind:checked={enabled} />
				<span>{m.subscriptions_wizard_enable_now()}</span>
			</label>
			{#if error}<div class="err">{uiText(error)}</div>{/if}
		</form>
	{/if}

	{#snippet actions()}
		{#if hasPreviewStep && urlStep === 'preview'}
			<Button variant="ghost" onclick={() => (urlStep = 'form')} disabled={submitting}>{m.subscriptions_wizard_back()}</Button>
		{:else if kind !== 'choose'}
			<Button variant="ghost" onclick={backToChoose} disabled={submitting}>{m.subscriptions_wizard_back()}</Button>
		{/if}
		<Button variant="ghost" onclick={close} disabled={submitting}>{m.common_cancel()}</Button>
		{#if kind === 'single'}
			<Button
				variant="primary"
				onclick={submitSingle}
				disabled={submitting || !singleLinks.trim() || !singboxInstalled}
				loading={submitting}
			>
				{submitting ? m.subscriptions_wizard_importing() : m.subscriptions_wizard_import()}
			</Button>
		{:else if hasPreviewStep && urlStep === 'form'}
			<Button
				variant="primary"
				onclick={fetchPreview}
				disabled={previewing || (kind === 'file' ? !filePath.trim() : !url.trim())}
				loading={previewing}
			>
				{previewing ? m.tunnels_loading() : m.tunnels_adopt_next()}
			</Button>
		{:else if hasPreviewStep && urlStep === 'preview'}
			<Button
				variant="primary"
				onclick={submitSubscription}
				disabled={submitting || previewMembers.length === excludedKeys.size}
				loading={submitting}
			>
				{submitting
					? m.subscriptions_wizard_creating()
					: m.subscriptions_wizard_create_keep({ count: previewMembers.length - excludedKeys.size })}
			</Button>
		{:else if kind !== 'choose'}
			<Button
				variant="primary"
				onclick={submitSubscription}
				disabled={submitting}
				loading={submitting}
			>
				{submitting ? m.subscriptions_wizard_creating() : m.common_create()}
			</Button>
		{/if}
	{/snippet}
</Modal>

<HappKeysModal
	bind:open={showHappKeysModal}
	onclose={() => (showHappKeysModal = false)}
	onsaved={() => {
		showHappKeysModal = false;
		if (url) triggerDetectHeaders(url, true);
	}}
/>

<FilePickerModal
	open={showFilePicker}
	onclose={() => (showFilePicker = false)}
	onpick={(p) => {
		filePath = p;
		showFilePicker = false;
	}}
/>

<style>
	.lead { color: var(--color-text-muted); font-size: 0.85rem; line-height: 1.5; margin: 0 0 0.8rem; }
	.lead code {
		background: var(--color-bg-tertiary, var(--color-bg-primary));
		padding: 0 4px;
		border-radius: 3px;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.78rem;
	}

	.kind-grid {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.6rem;
	}
	@media (min-width: 560px) {
		.kind-grid {
			grid-template-columns: repeat(2, minmax(12rem, 1fr));
		}
	}
	@media (min-width: 820px) {
		.kind-grid {
			/* auto-fit, а не repeat(4): с AWG3 видов пять, и на жёстких четырёх
			   колонках пятая карточка уходила на вторую строку одна. Пустые
			   треки auto-fit схлопывает, поэтому без AWG3 остаётся ровно
			   четыре колонки во всю ширину. */
			grid-template-columns: repeat(auto-fit, minmax(10.5rem, 1fr));
		}
	}
	.kind-card {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 1rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		text-align: left;
		cursor: pointer;
		font: inherit;
		color: var(--color-text-primary);
		transition: border-color 120ms, transform 120ms, background 120ms;
	}
	.kind-card:hover {
		border-color: var(--color-primary, #3b82f6);
		background: rgba(59, 130, 246, 0.04);
		transform: translateY(-1px);
	}
	.kind-card:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}

	.kind-title { font-weight: 500; font-size: 0.92rem; }
	.kind-desc { color: var(--color-text-muted); font-size: 0.78rem; line-height: 1.4; }

	.steps {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.74rem;
		color: var(--color-text-muted);
		margin-bottom: 0.4rem;
	}
	.step.current { color: var(--color-accent); font-weight: 600; }
	.step.done { color: var(--color-text-primary); }
	.step-sep { color: var(--color-text-muted); }

	.form { display: flex; flex-direction: column; gap: 1rem; }
	.row { display: flex; flex-direction: column; gap: 0.3rem; }
	.row.chk { flex-direction: row; align-items: center; gap: 0.5rem; }
	.row.two-col { flex-direction: row; gap: 0.75rem; }
	.col { flex: 1; display: flex; flex-direction: column; gap: 0.3rem; min-width: 0; }
	.lbl { font-size: 0.85rem; color: var(--color-text-muted); }
	.inp {
		padding: 0.5rem 0.7rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		color: var(--color-text-primary);
	}
	.inp.mono { font-family: var(--font-mono, ui-monospace, monospace); }
	.path-line { display: flex; align-items: stretch; gap: 0.4rem; }
	.path-line .inp { flex: 1; min-width: 0; }
	/* size="sm" жёстко фиксирует высоту кнопки (height/min/max: 28px), поэтому
	   align-items: stretch её не растягивает и низ кнопки не совпадает с низом
	   поля. Снимаем фиксацию — высоту задаёт строка. */
	.path-line :global(.btn) { height: auto; min-height: 0; max-height: none; }
	.hint {
		font-size: 0.74rem;
		color: var(--color-text-muted);
		line-height: 1.4;
		margin-top: 0.25rem;
	}
	.err { color: var(--color-error, #f85149); font-size: 0.85rem; }
	.err-list { margin: 0.4rem 0 0; padding-left: 1.2rem; }
	.warn {
		padding: 0.6rem 0.8rem;
		background: rgba(245, 158, 11, 0.08);
		border: 1px solid var(--warning, #d29922);
		border-radius: 4px;
		font-size: 0.82rem;
		color: var(--warning, #d29922);
	}

	.mode-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.5rem; }
	.mode-card {
		position: relative;
		text-align: left;
		padding: 0.6rem 0.75rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		color: var(--color-text-primary);
		cursor: pointer;
		font: inherit;
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		transition: border-color 120ms, background 120ms;
	}
	.mode-card:hover { border-color: var(--color-text-muted); }
	.mode-card.selected {
		border-color: var(--color-primary, #3b82f6);
		background: rgba(59, 130, 246, 0.06);
	}
	.mode-card:focus-visible {
		outline: 2px solid var(--color-primary, #3b82f6);
		outline-offset: 2px;
	}
	.mode-title { font-weight: 500; font-size: 0.85rem; }
	.mode-desc { font-size: 0.72rem; color: var(--color-text-muted); line-height: 1.35; }
	.mode-check {
		position: absolute;
		top: 0.5rem;
		right: 0.5rem;
		width: 14px;
		height: 14px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--color-primary, #3b82f6);
	}

	.urltest-block {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.6rem 0.8rem;
		background: var(--color-bg-secondary, var(--color-bg-primary));
		border: 1px dashed var(--color-border);
		border-radius: 4px;
	}
	.detect-badge {
		margin-top: 0.35rem;
		padding: 0.35rem 0.6rem;
		border-radius: 0.375rem;
		font-size: 0.8125rem;
		line-height: 1.35;
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}
	.detect-loading {
		background: rgba(59, 130, 246, 0.08);
		color: var(--color-primary, #3b82f6);
		border: 1px solid rgba(59, 130, 246, 0.25);
	}
	.detect-success {
		background: rgba(16, 185, 129, 0.08);
		color: var(--color-success, #10b981);
		border: 1px solid rgba(16, 185, 129, 0.2);
		font-weight: 500;
	}
	.detect-warning {
		background: rgba(245, 158, 11, 0.08);
		color: #d97706;
		border: 1px solid rgba(245, 158, 11, 0.2);
		font-weight: 500;
	}
	@media (max-width: 480px) {
		.mode-grid { grid-template-columns: 1fr; }
		.row.two-col { flex-direction: column; }
	}
</style>
