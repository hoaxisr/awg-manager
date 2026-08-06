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
	// СПИСОК WAN-ИНТЕРФЕЙСОВ ГРУЗИТ САМА КАРТОЧКА, и только когда он нужен —
	// при выключенном авто-определении. В шторке и в EngineSettingsCard запрос
	// уходил на каждый монтаж независимо от того, покажут ли селект.
	import { Card, Dropdown, Toggle, type DropdownOption } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxStatus } from '$lib/stores/singbox';
	import { systemInfo } from '$lib/stores/system';
	import DepRow from '$lib/components/sb-router/DepRow.svelte';
	import IssueRow from '$lib/components/sb-router/IssueRow.svelte';
	import { CRASH_WORDS } from '$lib/components/sb-router/crashInfo';
	import {
		planSelectWanInterface,
		planToggleAutoDetect,
		resolveWanAuto,
		type WanAutoOverride,
	} from '$lib/components/sb-router/wanMode';
	import { pluralForm } from '$lib/utils/pluralize';
	import { applyEngineSettings } from './engineSettings';
	import { normalizeRoutingMode } from './engineRunState';
	import { UDP_TIMEOUT_OPTIONS, engineCrashInfo, engineDeps, engineIssues } from './healthRows';
	import type { SingboxRouterWANInterface } from '$lib/types';

	const routerStatus = singboxRouter.status;
	const routerSettings = singboxRouter.settings;

	const s = $derived($routerStatus);
	const cfg = $derived($routerSettings);
	const mode = $derived(normalizeRoutingMode($routerSettings?.routingMode));

	const deps = $derived(engineDeps(s, mode));
	const issues = $derived(engineIssues(s, mode));
	const crash = $derived(engineCrashInfo(s));

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
	let wanOverride = $state<WanAutoOverride>(null);
	const wanAuto = $derived(resolveWanAuto(wanOverride, cfg?.wanAutoDetect));

	let wanInterfaces = $state<SingboxRouterWANInterface[]>([]);
	let wanRequested = false;
	$effect(() => {
		if (wanAuto || wanRequested) return;
		wanRequested = true;
		void loadWanInterfaces();
	});

	async function loadWanInterfaces(): Promise<void> {
		try {
			wanInterfaces = await api.singboxRouterListWANInterfaces();
		} catch (_e) {
			// Список — подсказка, а не условие правки: молча остаёмся с «— выберите —».
			wanRequested = false;
		}
	}

	const wanOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— выберите —' },
		...wanInterfaces.map((iface) => ({
			value: iface.name,
			label: iface.label ? `${iface.name} — ${iface.label}` : iface.name,
		})),
	]);

	function toggleWanAuto(checked: boolean): void {
		const { override, patch } = planToggleAutoDetect(checked);
		wanOverride = override;
		if (patch) void applyEngineSettings(patch);
	}

	function selectWanInterface(value: string): void {
		const action = planSelectWanInterface(value);
		if (!action) return;
		wanOverride = action.override;
		if (action.patch) void applyEngineSettings(action.patch);
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
			<div class="sec-cap">Замечания</div>

			{#each issues as issue, i (`${i}-${issue.text}`)}
				<IssueRow tone={issue.tone} text={issue.text} />
			{/each}

			{#if crash}
				<div class="crash">
					{#if crash.count > 0}
						<div class="stat-line">
							<span class="stat-label">Падений за 10 мин</span>
							<span class="stat-value">{crash.count}</span>
						</div>
					{/if}
					{#if crash.reason}
						<p class="crash-reason">Причина: {crash.reason}</p>
					{/if}
					{#if crash.suppressedUntil}
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
			<div class="field-row">
				<span>Определять WAN автоматически</span>
				<Toggle
					checked={wanAuto}
					ariaLabel="Определять WAN автоматически"
					onchange={toggleWanAuto}
				/>
			</div>
			{#if !wanAuto}
				<div class="field">
					<span class="lbl" id="eng-wan-label">Интерфейс</span>
					<Dropdown
						value={cfg.wanInterface ?? ''}
						options={wanOptions}
						fullWidth
						id="eng-wan"
						onchange={(v) => selectWanInterface(String(v))}
					/>
					<p class="hint">
						Выключение авто-определения сохранится вместе с выбранным интерфейсом: пустое
						значение бэкенд отклонит.
					</p>
				</div>
			{/if}
			<p class="hint">Через какой внешний интерфейс sing-box отправляет прямой трафик.</p>
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
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
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
	.empty {
		margin: 0;
		padding: 14px var(--sp-4);
		font-size: 12px;
		color: var(--text-muted);
	}
</style>
