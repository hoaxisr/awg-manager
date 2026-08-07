<script lang="ts">
	// Карточка «Здоровье» страницы «Движок»: зависимости, замечания, блок
	// падений (#456), версия sing-box, определение WAN, sniffer и UDP-таймаут.
	// Источник — StatusDrawer.svelte:351-373 (падения), :396-411 (зависимости и
	// замечания), :432-476 (WAN, sniffer, UDP), :95-101 (версия).
	//
	// ЧТО ЗДЕСЬ ИСПРАВЛЕНО ПРОТИВ ШТОРКИ:
	//
	//  * Текст блока падений. В шторке он говорил «кнопка „Перезапустить“ НИЖЕ»
	//    — она жила в футере шторки. На странице «Движок» кнопка в шапке, то
	//    есть выше карточки; дословный перенос отправлял бы искать её не там.
	//
	//  * Дедупликация policy-tun-unbound и режимный гейт зависимости «TPROXY
	//    target» — см. healthRows.ts, там же разбор причин.
	//
	//  * Гейта по режиму «Эксперт» нет: сам режим в nav-v3 умер (§2.1 спеки), а
	//    WAN, sniffer и UDP-таймаут — единственные места правки этих полей.
	//
	//  * Блок падений молчит про паузу и счётчик, когда рядом стоит замечание
	//    engine-dead-interception: бэкенд вкладывает оба факта прямо в его текст
	//    (service_lifecycle.go), и на странице они печатались встык дважды.
	//    В шторке их разделяли две секции.
	//
	// СПИСОК WAN-ИНТЕРФЕЙСОВ ГРУЗИТ САМА КАРТОЧКА, один раз на монтаж. Ленивой
	// загрузки больше нет: список — единственный способ задать WAN, и нужен он
	// всегда, а не только при выключенном авто-определении.
	import { Badge, Card, Dropdown, Toggle, type DropdownOption } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxStatus } from '$lib/stores/singbox';
	import { systemInfo } from '$lib/stores/system';
	import DepRow from '$lib/components/sb-router/DepRow.svelte';
	import IssueRow from '$lib/components/sb-router/IssueRow.svelte';
	import { CRASH_WORDS } from '$lib/components/sb-router/crashInfo';
	import { pluralForm } from '$lib/utils/pluralize';
	import { applyEngineSettings } from './engineSettings';
	import { normalizeRoutingMode } from './engineRunState';
	import { UDP_TIMEOUT_OPTIONS, engineCrashInfo, engineDeps, engineIssues } from './healthRows';
	import { planWanSelection, wanSelectOptions, wanSelectValue } from './wanSelect';
	import type { SingboxRouterWANInterface } from '$lib/types';

	const routerStatus = singboxRouter.status;
	const routerSettings = singboxRouter.settings;

	const s = $derived($routerStatus);
	const cfg = $derived($routerSettings);
	const mode = $derived(normalizeRoutingMode($routerSettings?.routingMode));

	const deps = $derived(engineDeps(s, mode));
	const issues = $derived(engineIssues(s, mode));
	const crash = $derived(engineCrashInfo(s, issues));

	// Версия: сначала статус установки sing-box, затем сводка системы — та же
	// цепочка, что была в шапке шторки.
	const sbVersion = $derived(
		(
			$singboxStatus.data?.version ??
			$singboxStatus.data?.currentVersion ??
			$systemInfo.data?.singbox?.version ??
			''
		).trim(),
	);

	// ── WAN ────────────────────────────────────────────────────────────────
	let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);
	let wanFailed = $state(false);
	// Обычный `let`, а НЕ `$state`: латч «запрос уже уходил». Сделай его
	// реактивным и сбрось в catch — эффект перезапустится сам и будет долбить
	// упавшую ручку без остановки. Повтор — только по кнопке.
	let wanRequested = false;
	$effect(() => {
		if (wanRequested) return;
		wanRequested = true;
		void loadWanInterfaces();
	});

	async function loadWanInterfaces(): Promise<void> {
		try {
			wanInterfaces = await api.singboxRouterListWANInterfaces();
			wanFailed = false;
		} catch (_e) {
			// Молчать нельзя: без списка пользователь не видит своих интерфейсов
			// и не понимает, почему выбрать нечего.
			wanFailed = true;
		}
	}

	const wanValue = $derived(wanSelectValue(cfg));
	const wanOptions = $derived<DropdownOption[]>(wanSelectOptions(wanInterfaces, wanValue));

	function selectWan(value: string): void {
		const patch = planWanSelection(value, wanValue);
		if (patch) void applyEngineSettings(patch);
	}

	// ── Анализ трафика ─────────────────────────────────────────────────────
	const udpOptions = $derived<DropdownOption[]>(UDP_TIMEOUT_OPTIONS);
</script>

<Card padding="none">
	<div class="head-row">
		<div class="title">Здоровье</div>
		<div class="sub">Готовность окружения, замечания и общие настройки движка</div>
	</div>

	<section class="sec">
		<div class="sec-cap">Зависимости</div>
		{#if deps.length === 0}
			<p class="hint">Статус движка ещё не загружен.</p>
		{:else}
			<div class="deps">
				{#each deps as dep (dep.id)}
					<DepRow tone={dep.tone} label={dep.label} hint={dep.hint} />
				{/each}
			</div>
		{/if}
		<div class="stat-line">
			<span class="stat-label">Версия sing-box</span>
			<span class="stat-value">{sbVersion ? `v${sbVersion}` : '—'}</span>
		</div>
	</section>

	{#if issues.length > 0 || crash}
		<section class="sec">
			<div class="sec-cap">
				Замечания
				{#if issues.length > 0}
					<Badge variant="warning" size="sm">{issues.length}</Badge>
				{/if}
			</div>

			{#each issues as issue, i (`${i}-${issue.text}`)}
				<IssueRow tone={issue.tone} text={issue.text} />
			{/each}

			{#if crash}
				<div class="crash">
					<!-- `restated` — счётчик и пауза уже напечатаны текстом замечания
					     engine-dead-interception строкой выше. Причина в тот текст не
					     входит и остаётся здесь при любом раскладе. -->
					{#if crash.count > 0 && !crash.restated}
						<div class="stat-line">
							<span class="stat-label">Падений за 10 мин</span>
							<span class="stat-value">{crash.count}</span>
						</div>
					{/if}
					{#if crash.reason}
						<p class="crash-reason">Причина: {crash.reason}</p>
					{/if}
					{#if crash.restated}
						<p class="crash-hint">
							Кнопка «Перезапустить» в шапке страницы запускает движок немедленно.
						</p>
					{:else if crash.suppressedUntil}
						<p class="crash-suppressed">
							Автоперезапуск приостановлен до {crash.suppressedUntil}{#if crash.count > 0}&nbsp;({crash.count}
								{pluralForm(crash.count, CRASH_WORDS)} за 10 мин){/if}. Кнопка «Перезапустить» в
							шапке страницы запускает движок немедленно.
						</p>
					{/if}
				</div>
			{/if}
		</section>
	{/if}

	{#if cfg}
		<section class="sec">
			<div class="sec-cap">Внешний интерфейс</div>
			<!-- Один список вместо «тумблер + зависимый селект»: пара
			     (wanAutoDetect, wanInterface) у бэкенда жёстко связана, и тумблер
			     выражал её невалидную половину. Проп `label` рисует настоящий
			     <label for> — прежний <span id="eng-wan-label"> ни на что не
			     ссылался. -->
			<Dropdown
				value={wanValue}
				options={wanOptions}
				label="Интерфейс"
				fullWidth
				id="eng-wan"
				onchange={(v) => selectWan(String(v))}
			/>
			{#if wanFailed}
				<p class="load-error">
					Не удалось загрузить список интерфейсов роутера.
					<button type="button" class="retry" onclick={() => void loadWanInterfaces()}>
						Повторить
					</button>
				</p>
			{/if}
			<p class="hint">
				Через какой внешний интерфейс sing-box отправляет прямой трафик. «Автоматически» —
				движок берёт текущий WAN роутера сам.
			</p>
		</section>

		<section class="sec">
			<div class="sec-cap">Анализ трафика</div>
			<div class="field-row">
				<span>Включить sniff</span>
				<Toggle
					checked={cfg.snifferEnabled}
					ariaLabel="Включить sniff"
					onchange={(checked) => void applyEngineSettings({ snifferEnabled: checked })}
				/>
			</div>
			<p class="hint">
				Анализ HTTP/TLS/QUIC по содержимому. Улучшает срабатывание domain-based правил при
				IP-only matchers.
			</p>

			<div class="field">
				<span class="lbl">UDP-таймаут сессии</span>
				<Dropdown
					value={cfg.udpTimeout ?? ''}
					options={udpOptions}
					fullWidth
					id="eng-udp-timeout"
					onchange={(v) => void applyEngineSettings({ udpTimeout: String(v) || undefined })}
				/>
			</div>
			<p class="hint">
				Как долго sing-box держит UDP-сессии активными. Увеличьте, если игры или другие
				UDP-приложения обрываются каждые несколько минут.
			</p>
		</section>
	{:else}
		<p class="empty">Настройки движка ещё не загружены.</p>
	{/if}
</Card>

<style>
	.head-row {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 12px var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.title {
		font-size: 15px;
		font-weight: 700;
		color: var(--text-primary);
	}
	.sub {
		font-size: 12px;
		line-height: 1.4;
		color: var(--text-muted);
	}
	.sec {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 14px var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.sec:last-of-type {
		border-bottom: 0;
	}
	.sec-cap {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}
	/* Badge — child-компонент, scoped-CSS его не цепляет: контейнер-scoped
	   :global, та же идиома, что у иконок в соседних карточках. */
	.sec-cap :global(.badge) {
		text-transform: none;
		letter-spacing: 0;
	}
	/* Зависимости строкой (макет engine-design.html): чипы в ряд с переносом,
	   а не колонка широких строк — их две, и на всю ширину карточки они
	   выглядели бы как список проблем. */
	.deps {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.field-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		font-size: 13px;
	}
	.field-row > span {
		flex: 1;
		min-width: 0;
	}
	.lbl {
		font-size: 11px;
		color: var(--text-muted);
		font-weight: 500;
	}
	/* Поток текста, а НЕ flex: во flex-контейнере знаки препинания после
	   вложенных элементов уезжают на gap (та же ловушка, что в соседних
	   карточках). */
	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.stat-line {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		font-size: 12px;
	}
	.stat-label {
		color: var(--text-muted);
	}
	.stat-value {
		color: var(--text-secondary);
		font-family: var(--font-mono);
		font-size: 11.5px;
		text-align: right;
	}
	.crash {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-left: 3px solid var(--color-warning, #dab856);
	}
	.crash-reason {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-secondary);
		line-height: 1.4;
		word-break: break-word;
	}
	.crash-suppressed {
		margin: 0;
		font-size: 11.5px;
		color: var(--color-warning, #dab856);
		line-height: 1.4;
	}
	/* Подсказка про кнопку, когда пауза уже сказана текстом замечания. Тон
	   нейтральный: сам факт паузы отсюда ушёл, а «где кнопка» — не warning. */
	.crash-hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}
	/* Отказ загрузки списка: поток текста, кнопка внутри строки. */
	.load-error {
		margin: 0;
		font-size: 11.5px;
		line-height: 1.4;
		color: var(--color-error, #d05b5b);
	}
	.retry {
		padding: 0;
		border: 0;
		background: none;
		font: inherit;
		color: var(--color-accent, #5b9bd0);
		text-decoration: underline;
		cursor: pointer;
	}
	.empty {
		margin: 0;
		padding: 14px var(--sp-4);
		font-size: 12px;
		color: var(--text-muted);
	}
</style>
