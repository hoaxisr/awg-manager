<script lang="ts" module>
	export type PremiumWizardBackend = 'nativewg' | 'kernel';

	/** Что мастер отдаёт вызывающему после успешной выдачи конфигурации. */
	export interface PremiumWizardResult {
		countryCode: string;
		/** Текст .conf; строки с ключом подписки бэкенд из него уже вырезал. */
		config: string;
		suggestedName: string;
		/** В режиме замены поля нет: бэкенд существующего туннеля мастер не выбирает. */
		backend?: PremiumWizardBackend;
	}

	/** Туннель, на который нацелена замена конфигурации. */
	export interface PremiumWizardReplaceTarget {
		id: string;
		name: string;
		/** Страна подписки, которой туннель помечен сейчас. */
		country?: string;
	}
</script>

<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import Button from '$lib/components/ui/Button.svelte';
	import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';
	import Modal from '$lib/components/ui/Modal.svelte';
	import type { AmneziaPremiumCatalog } from '$lib/types';
	import {
		isPremiumCountryAvailable,
		isPremiumCountryIssued,
		isPremiumIssueAllowed,
		premiumActiveDevicesForCountry,
		premiumSubscriptionState
	} from '$lib/utils/amneziaPremiumCatalog';
	import PremiumCountryList from './PremiumCountryList.svelte';
	import PremiumCreateFooter from './PremiumCreateFooter.svelte';
	import PremiumKeyForm from './PremiumKeyForm.svelte';
	import PremiumSubscriptionCard from './PremiumSubscriptionCard.svelte';
	// Временный блок миграции ключа из localStorage — снимается целиком, см. F276.
	import { clearLegacyPremiumKeys, readLegacyPremiumKey } from './premiumKeyMigration';

	interface Props {
		open: boolean;
		/** Задан — режим замены конфигурации; отсутствует — режим создания туннеля. */
		replaceTarget?: PremiumWizardReplaceTarget | null;
		/** «Страна → туннель» списком: из него берётся метка «туннель awg-nl». */
		countryTunnels?: readonly { name: string; amneziaCountry?: string }[];
		/**
		 * Доступность бэкендов — с уже загруженного страницей system/info.
		 * Отсутствует — доступность неизвестна (как на странице создания
		 * туннеля до ответа), и гасить выбор нечем.
		 */
		backendAvailability?: { nativewg: boolean; kernel: boolean };
		onclose: () => void;
		onconfig: (result: PremiumWizardResult) => void;
	}

	let {
		open,
		replaceTarget = null,
		countryTunnels = [],
		backendAvailability,
		onclose,
		onconfig
	}: Props = $props();

	type Phase = 'key' | 'loading' | 'catalog' | 'error';
	type RetryKind = 'init' | 'login' | 'catalog';

	let phase = $state<Phase>('loading');
	let keyInput = $state('');
	let remember = $state(false);
	let keyStored = $state(false);
	let keyUsable = $state(false);
	let saveWarning = $state('');
	let catalog = $state<AmneziaPremiumCatalog | null>(null);
	/** Отметка времени на момент загрузки каталога: срок подписки не должен «ехать» при перерисовках. */
	let nowMs = $state(0);
	let selectedCountry = $state('');
	let tunnelName = $state('');
	let nameEdited = $state(false);
	let backend = $state<PremiumWizardBackend>('nativewg');
	let errorText = $state('');
	let errorHint = $state('');
	let retryKind = $state<RetryKind>('init');
	let retryLabel = $state('Повторить');
	let busy = $state(false);
	let confirmCountry = $state('');

	// Поколение загрузки. Намеренно НЕ $state: значение нигде не рисуется, а
	// реактивным оно сделало бы зависимым от себя эффект, который его же
	// увеличивает. Тот же приём и по той же причине — в VpnLinkPasteImport.
	let loadGen = 0;

	/** Гасит летящий ответ: всё, что придёт со старым поколением, отбрасывается. */
	function cancelInFlight(): void {
		loadGen++;
	}

	function isStale(gen: number): boolean {
		return gen !== loadGen;
	}

	const issuedConfigs = $derived(catalog?.issuedConfigs ?? []);
	const subscriptionState = $derived(
		catalog ? premiumSubscriptionState(catalog.subscriptionEndDate, nowMs) : 'unknown'
	);
	const issueAllowed = $derived(catalog !== null && isPremiumIssueAllowed(subscriptionState));
	const confirmDevices = $derived(
		confirmCountry ? premiumActiveDevicesForCountry(issuedConfigs, confirmCountry).length : 0
	);
	const confirmCountryName = $derived(
		catalog?.countries.find((c) => c.code === confirmCountry)?.name ?? confirmCountry
	);
	const title = $derived(
		replaceTarget ? `Amnezia Premium → ${replaceTarget.name}` : 'Amnezia Premium'
	);
	const canSubmitKey = $derived(keyInput.trim().length > 0 && !busy);

	const nativewgAvailable = $derived(backendAvailability?.nativewg !== false);
	const kernelAvailable = $derived(backendAvailability?.kernel !== false);
	/**
	 * Бэкенд, который уедет в импорт. Недоступный выбранным не остаётся:
	 * выдача конфигурации тратит слот устройств подписки ДО того, как импорт
	 * откажет, и слот этот не возвращается (F277).
	 */
	const chosenBackend = $derived<PremiumWizardBackend>(
		backend === 'nativewg' && !nativewgAvailable
			? 'kernel'
			: backend === 'kernel' && !kernelAvailable
				? 'nativewg'
				: backend
	);

	const canIssue = $derived(
		selectedCountry !== '' &&
			issueAllowed &&
			!busy &&
			(replaceTarget !== null ||
				// Ни одного доступного бэкенда — выдавать нечего: импорт откажет,
				// а слот подписки уже потрачен.
				(tunnelName.trim().length > 0 && (nativewgAvailable || kernelAvailable)))
	);

	// Инициализация привязана к переходу «закрыт → открыт». Единственная
	// отслеживаемая зависимость здесь — сам `open`: всё остальное читается
	// внутри initialize вне отслеживания.
	$effect(() => {
		if (open) {
			void initialize();
		} else {
			// Гашение при закрытии: ответ, пришедший закрытому мастеру, не
			// должен дописывать ничего в его состояние.
			cancelInFlight();
		}
	});

	async function initialize(): Promise<void> {
		const gen = ++loadGen;
		catalog = null;
		selectedCountry = '';
		tunnelName = '';
		nameEdited = false;
		// Состояние ключа обнуляется до ответа: «ключ сохранён» — утверждение,
		// и держать его с прошлого открытия значит утверждать непроверенное.
		keyStored = false;
		keyUsable = false;
		remember = false;
		saveWarning = '';
		confirmCountry = '';
		errorText = '';
		errorHint = '';
		busy = false;
		phase = 'loading';

		// Поле ключа читается ВНЕ ОТСЛЕЖИВАНИЯ. Отслеживаемое чтение сделало бы
		// этот эффект зависимым от поля, и каждый введённый символ перезапускал
		// бы инициализацию — на одну вставку ключа роутер получил бы сотню
		// запросов состояния ключа.
		const typed = untrack(() => keyInput.trim());
		// Введённое пользователем миграционным ключом не затираем.
		if (!typed) {
			const legacy = readLegacyPremiumKey();
			if (legacy) keyInput = legacy;
		}

		try {
			const state = await api.amneziaPremiumKeyState();
			if (isStale(gen)) return;
			keyStored = state.stored;
			keyUsable = state.usable;
			if (state.stored && state.usable) {
				await loadCatalog(gen);
				return;
			}
			phase = 'key';
		} catch (e) {
			if (isStale(gen)) return;
			failWith(e, 'init');
		}
	}

	async function loadCatalog(gen: number): Promise<void> {
		phase = 'loading';
		try {
			const data = await api.amneziaPremiumCatalog();
			if (isStale(gen)) return;
			catalog = data;
			nowMs = Date.now();
			// В режиме замены страна туннеля уже известна — подставляем её,
			// чтобы «Заменить конфиг» не требовал искать её в списке заново.
			// Только если она в списке ЕСТЬ: подписка могла её потерять или
			// отдавать одним vless, и тогда выбранной оказалась бы строка,
			// которой на экране нет, — с активной кнопкой замены.
			if (replaceTarget?.country && shownCountryCode(data, replaceTarget.country)) {
				chooseCountry(replaceTarget.country);
			}
			phase = 'catalog';
		} catch (e) {
			if (isStale(gen)) return;
			failWith(e, 'catalog');
		}
	}

	async function submitKey(): Promise<void> {
		const key = keyInput.trim();
		if (!key) return;
		const gen = ++loadGen;
		busy = true;
		phase = 'loading';
		try {
			const state = await api.amneziaPremiumSaveKey(key, { store: remember });
			if (isStale(gen)) return;
			clearLegacyPremiumKeys();
			keyStored = state.stored;
			keyUsable = state.usable;
			saveWarning = state.saveError ?? '';
			// Ключ проверен — держать его в поле больше незачем.
			keyInput = '';
			busy = false;
			await loadCatalog(gen);
		} catch (e) {
			if (isStale(gen)) return;
			busy = false;
			failWith(e, 'login');
		}
	}

	async function forgetKey(): Promise<void> {
		// Поколение ЗАХВАТЫВАЕТСЯ, а не увеличивается: гасит летящий каталог
		// сам сброс (resetToKeyEntry), и проверять это надо там.
		const gen = loadGen;
		busy = true;
		try {
			await api.amneziaPremiumForgetKey();
		} catch {
			// Отказ удаления ключа всё равно ведёт к вводу заново: состояние
			// ключа после него неизвестно, а каталог показывать уже нельзя.
		}
		if (isStale(gen)) return;
		keyStored = false;
		keyUsable = false;
		resetToKeyEntry();
	}

	/**
	 * Сброс к вводу ключа: «забыть ключ» и «ввести другой ключ».
	 *
	 * Гашение здесь обязательно и отдельно от гашения при закрытии: каталог,
	 * долетевший после сброса, воскресил бы список стран уже забытой подписки
	 * — вместе с активной кнопкой выдачи.
	 */
	function resetToKeyEntry(): void {
		cancelInFlight();
		catalog = null;
		selectedCountry = '';
		tunnelName = '';
		nameEdited = false;
		keyInput = '';
		errorText = '';
		errorHint = '';
		saveWarning = '';
		confirmCountry = '';
		busy = false;
		phase = 'key';
	}

	function premiumErrorCode(e: unknown): string {
		const body = (e as { body?: { code?: unknown } } | null)?.body;
		return typeof body?.code === 'string' ? body.code : '';
	}

	/**
	 * Подсказка под сообщением бэкенда. Сам текст отказа пишет бэкенд — здесь
	 * только то, что следует делать дальше, и только там, где это не очевидно.
	 */
	function premiumErrorHint(code: string): string {
		switch (code) {
			case 'AMNEZIA_PREMIUM_FORBIDDEN':
				// Ключ менять не предлагаем: 403 — запрет операции, а не приговор
				// ключу, и замена рабочего ключа лимит устройств не вернёт.
				return 'Откройте список стран и проверьте счётчик устройств подписки — ключ при этом менять не нужно.';
			case 'AMNEZIA_PREMIUM_KEY_REJECTED':
				return 'Похоже, ключ подписки больше не действует — введите другой.';
			case 'AMNEZIA_PREMIUM_CONFIG_BUSY':
				return 'Эта страна уже выдаётся — дождитесь ответа и обновите список стран.';
			default:
				// AMNEZIA_PREMIUM_OUTCOME_UNKNOWN сюда тоже попадает намеренно:
				// звать что-либо делать после неизвестного исхода расходной
				// операции нельзя, а всё нужное уже сказано в тексте бэкенда.
				return '';
		}
	}

	function failWith(e: unknown, source: 'init' | 'login' | 'catalog' | 'config'): void {
		errorText = e instanceof Error ? e.message : 'Сервис Amnezia недоступен';
		errorHint = premiumErrorHint(premiumErrorCode(e));
		if (source === 'config') {
			// Повтор расходной выдачи одной кнопкой не предлагается никогда: он
			// тратит второй слот устройств. Возврат к списку стран — операция
			// читающая, и он же показывает счётчик, по которому видно, была ли
			// выдача на самом деле.
			retryKind = 'catalog';
			retryLabel = 'К списку стран';
		} else {
			retryKind = source;
			retryLabel = 'Повторить';
		}
		phase = 'error';
	}

	function retry(): void {
		if (retryKind === 'login') {
			void submitKey();
			return;
		}
		if (retryKind === 'catalog') {
			void loadCatalog(++loadGen);
			return;
		}
		void initialize();
	}

	/** Код страны, если она действительно показана в списке; иначе пустая строка. */
	function shownCountryCode(data: AmneziaPremiumCatalog, code: string): string {
		const wanted = code.trim().toLowerCase();
		const hit = data.countries.find(
			(c) => c.code.trim().toLowerCase() === wanted && isPremiumCountryAvailable(c)
		);
		return hit ? hit.code : '';
	}

	function chooseCountry(code: string): void {
		selectedCountry = code;
		if (!nameEdited) tunnelName = code ? `awg-${code.toLowerCase()}` : '';
	}

	function requestConfig(): void {
		if (!selectedCountry || !issueAllowed) return;
		if (isPremiumCountryIssued(issuedConfigs, selectedCountry)) {
			confirmCountry = selectedCountry;
			return;
		}
		void issueConfig(selectedCountry);
	}

	async function issueConfig(code: string): Promise<void> {
		const gen = loadGen;
		busy = true;
		try {
			const cfg = await api.amneziaPremiumConfig(code);
			if (isStale(gen)) return;
			busy = false;
			confirmCountry = '';
			onconfig({
				countryCode: cfg.countryCode || code,
				config: cfg.config,
				suggestedName: replaceTarget ? replaceTarget.name : tunnelName.trim(),
				backend: replaceTarget ? undefined : chosenBackend
			});
			onclose();
		} catch (e) {
			if (isStale(gen)) return;
			busy = false;
			confirmCountry = '';
			failWith(e, 'config');
		}
	}
</script>

{#snippet wizardBody()}
	{#if phase === 'key'}
		<PremiumKeyForm
			value={keyInput}
			{remember}
			{busy}
			unusableStored={keyStored && !keyUsable}
			oninput={(v) => (keyInput = v)}
			onremember={(v) => (remember = v)}
			onforget={() => void forgetKey()}
		/>
	{:else if phase === 'loading'}
		<div class="premium-skeletons" aria-busy="true">
			<div class="premium-skeleton premium-skeleton--card"></div>
			<div class="premium-skeleton premium-skeleton--row"></div>
			<div class="premium-skeleton premium-skeleton--row"></div>
			<div class="premium-skeleton premium-skeleton--row"></div>
		</div>
	{:else if phase === 'error'}
		<div class="premium-error">
			<p class="premium-error-text">{errorText}</p>
			{#if errorHint}
				<p class="premium-error-hint">{errorHint}</p>
			{/if}
		</div>
	{:else if catalog}
		<div class="premium-catalog">
			<PremiumSubscriptionCard {catalog} {nowMs} />
			{#if saveWarning}
				<p class="premium-save-warning">
					Ключ проверен, но сохранить его на роутере не вышло: {saveWarning}
				</p>
			{/if}
			<PremiumCountryList
				countries={catalog.countries}
				issued={issuedConfigs}
				{countryTunnels}
				selected={selectedCountry}
				disabled={!issueAllowed || busy}
				onselect={chooseCountry}
			/>
		</div>
	{/if}
{/snippet}

{#snippet wizardActions()}
	{#if phase === 'key'}
		<Button variant="secondary" size="md" onclick={onclose}>Отмена</Button>
		<Button variant="primary" size="md" disabled={!canSubmitKey} onclick={() => void submitKey()}>
			Продолжить
		</Button>
	{:else if phase === 'error'}
		<Button variant="secondary" size="md" onclick={onclose}>Закрыть</Button>
		<Button variant="secondary" size="md" onclick={retry}>{retryLabel}</Button>
		<!-- Без этого действия пользователь с отозванным сохранённым ключом
		     заперт: вкладки со вставкой vpn:// больше нет. -->
		<Button variant="primary" size="md" onclick={resetToKeyEntry}>Ввести другой ключ</Button>
	{:else}
		<div class="premium-footer">
			{#if keyStored && keyUsable}
				<!-- Доступна и при загрузке: каталог может висеть на недоступном
				     зеркале, и запирать выход из подписки до его ответа нельзя. -->
				<Button variant="ghost" size="md" disabled={busy} onclick={() => void forgetKey()}>
					Забыть ключ
				</Button>
			{/if}
			{#if !replaceTarget}
				<PremiumCreateFooter
					name={tunnelName}
					backend={chosenBackend}
					{nativewgAvailable}
					{kernelAvailable}
					disabled={phase !== 'catalog' || busy}
					onname={(v) => {
						nameEdited = true;
						tunnelName = v;
					}}
					onbackend={(v) => (backend = v)}
				/>
			{/if}
			<Button
				variant="primary"
				size="md"
				disabled={phase !== 'catalog' || !canIssue}
				onclick={requestConfig}
			>
				{replaceTarget ? 'Заменить конфиг' : 'Создать туннель'}
			</Button>
		</div>
	{/if}
{/snippet}

<Modal
	{open}
	{title}
	size="lg"
	onclose={onclose}
	closeOnBackdrop={false}
	children={wizardBody}
	actions={wizardActions}
/>

<ConfirmModal
	open={confirmCountry !== ''}
	title="Выдать конфигурацию повторно?"
	message={`По стране «${confirmCountryName}» конфигурация уже выдавалась. Повторная выдача тратит слот устройств подписки.`}
	secondary={`Активных устройств по этой стране: ${confirmDevices}.`}
	confirmLabel="Выдать повторно"
	cancelLabel="Отмена"
	variant="primary"
	{busy}
	onConfirm={() => void issueConfig(confirmCountry)}
	onClose={() => (confirmCountry = '')}
/>

<style>
	.premium-catalog {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.premium-save-warning {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--warning, var(--color-warning));
	}

	.premium-skeletons {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.premium-skeleton {
		border-radius: 8px;
		background: var(--bg-secondary, var(--color-bg-secondary));
		border: 1px solid var(--border, var(--color-border));
	}

	.premium-skeleton--card {
		height: 62px;
	}

	.premium-skeleton--row {
		height: 34px;
	}

	.premium-error {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.premium-error-text {
		margin: 0;
		font-size: 0.875rem;
		color: var(--error, var(--color-error));
	}

	.premium-error-hint {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-secondary, var(--color-text-secondary));
	}

	/* Подвал каталога: на узком экране (~400px) переносится в две строки, а
	   кнопки растягивает на всю ширину сама модалка (правило ≤640px). */
	.premium-footer {
		display: flex;
		flex: 1;
		flex-wrap: wrap;
		align-items: center;
		justify-content: flex-end;
		gap: 8px;
	}
</style>
