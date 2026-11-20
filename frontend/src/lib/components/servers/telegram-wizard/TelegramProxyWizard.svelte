<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import WizardShell from '../wizard/WizardShell.svelte';
	import PreflightList from '../wizard/PreflightList.svelte';
	import PlanSummary from '../wizard/PlanSummary.svelte';
	import ApplyProgress from '../wizard/ApplyProgress.svelte';
	import ConnectionResult from '../wizard/ConnectionResult.svelte';
	import type {
		CapabilitiesResponse,
		PreflightResponse,
		ServerPlanRecord,
		JobStatusResponse,
		RevealCredentials,
		WizardPlanRequest
	} from '$lib/types/serverWizard';
	import { Shield, Globe, Zap } from 'lucide-svelte';

	interface Props {
		open: boolean;
		onclose: () => void;
		onapplied?: () => void;
	}

	let { open = $bindable(false), onclose, onapplied }: Props = $props();

	const steps = [
		{ id: 'scenario', label: 'Сценарий' },
		{ id: 'cdn', label: 'Домен и CDN' },
		{ id: 'preflight', label: 'Проверка' },
		{ id: 'plan', label: 'План' },
		{ id: 'apply', label: 'Применение' },
		{ id: 'result', label: 'Подключение' }
	];

	let currentStep = $state(0);
	let loading = $state(false);

	// Capabilities
	let caps = $state<CapabilitiesResponse | null>(null);
	let capsError = $state<string | null>(null);

	// Form State
	let scenario = $state<'dual' | 'direct_fake_tls' | 'cdn_http'>('dual');
	let upstreamDevice = $state('direct');
	let publicDomain = $state('');
	let cdnProfileId = $state('cdn_get');
	let directHost = $state('');
	let directPort = $state(8443);
	let tlsDomain = $state('');
	let listenPort = $state(8085);

	// Wizard Process State
	let preflight = $state<PreflightResponse | null>(null);
	let planRecord = $state<ServerPlanRecord | null>(null);
	let currentJob = $state<JobStatusResponse | null>(null);
	let revealedCreds = $state<RevealCredentials | null>(null);
	let pollTimer: ReturnType<typeof setInterval> | null = null;
	let cancelling = $state(false);

	$effect(() => {
		if (open) {
			void initWizard();
		} else {
			cleanup();
		}
	});

	function cleanup() {
		if (pollTimer) {
			clearInterval(pollTimer);
			pollTimer = null;
		}
		currentStep = 0;
		loading = false;
		capsError = null;
		preflight = null;
		planRecord = null;
		currentJob = null;
		revealedCreds = null;
		cancelling = false;
	}

	async function initWizard() {
		loading = true;
		capsError = null;
		try {
			caps = await api.getWizardCapabilities('tgwebproxy');
			if (caps && Array.isArray(caps.egress_options) && caps.egress_options.length > 0) {
				const hasDefault = caps.egress_options.some((o) => o.id === upstreamDevice);
				if (!hasDefault) {
					upstreamDevice = caps.egress_options[0].id;
				}
			}
		} catch (e) {
			const msg = e instanceof Error ? e.message : 'Не удалось загрузить параметры мастера';
			capsError = msg;
			notifications.error(msg);
		} finally {
			loading = false;
		}
	}

	function buildRequest(): WizardPlanRequest {
		return {
			kind: 'tgwebproxy',
			scenario,
			public_domain: publicDomain.trim(),
			cdn_profile_id: cdnProfileId,
			direct_host: directHost.trim(),
			direct_port: directPort,
			tls_domain: tlsDomain.trim(),
			listen_port: listenPort,
			upstream_device: upstreamDevice
		};
	}

	async function goNext() {
		if (currentStep === 1) {
			// Step 1 -> 2: Run preflight
			loading = true;
			try {
				preflight = await api.runWizardPreflight('tgwebproxy', buildRequest());
				currentStep = 2;
			} catch (e) {
				notifications.error(e instanceof Error ? e.message : 'Ошибка проверки готовности');
			} finally {
				loading = false;
			}
		} else if (currentStep === 2) {
			// Step 2 -> 3: Generate plan
			if (!preflight?.can_proceed) {
				notifications.error('Исправьте блокирующие ошибки перед продолжением');
				return;
			}
			loading = true;
			try {
				planRecord = await api.createWizardPlan('tgwebproxy', buildRequest());
				currentStep = 3;
			} catch (e) {
				notifications.error(e instanceof Error ? e.message : 'Ошибка создания плана изменений');
			} finally {
				loading = false;
			}
		} else if (currentStep === 3) {
			// Step 3 -> 4: Apply plan
			if (!planRecord) return;
			loading = true;
			try {
				const res = await api.applyWizardPlan('tgwebproxy', planRecord.plan_id);
				currentStep = 4;
				startPolling(res.job_id);
			} catch (e: unknown) {
				const errMsg = e instanceof Error ? e.message : String(e);
				if (errMsg.includes('PLAN_STALE') || errMsg.includes('устарел')) {
					notifications.error('Конфигурация роутера изменилась, план устарел. Пожалуйста, выполните повторную проверку.');
					currentStep = 1;
				} else {
					notifications.error(errMsg || 'Ошибка применения плана');
				}
			} finally {
				loading = false;
			}
		} else if (currentStep === 4) {
			// Finished apply -> result
			currentStep = 5;
		} else {
			currentStep++;
		}
	}

	function goBack() {
		if (currentStep > 0 && currentStep < 4) {
			currentStep--;
		}
	}

	function startPolling(jobId: string) {
		if (pollTimer) clearInterval(pollTimer);
		pollTimer = setInterval(async () => {
			try {
				const job = await api.getWizardJob('tgwebproxy', jobId);
				currentJob = job;
				if (job.phase === 'succeeded') {
					if (pollTimer) clearInterval(pollTimer);
					pollTimer = null;
					currentStep = 5;
					onapplied?.();
					void revealSecrets(jobId);
				} else if (job.phase === 'failed' || job.phase === 'cancelled' || job.phase === 'recovery_required') {
					if (pollTimer) clearInterval(pollTimer);
					pollTimer = null;
				}
			} catch {
				// Ignore transient network errors
			}
		}, 800);
	}

	async function cancelApply() {
		if (!currentJob?.id) return;
		cancelling = true;
		try {
			await api.cancelWizardJob('tgwebproxy', currentJob.id);
			notifications.info('Запрос на отмену отправлен');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось отменить операцию');
		} finally {
			cancelling = false;
		}
	}

	async function revealSecrets(jobId?: string) {
		const targetId = jobId || currentJob?.id;
		if (!targetId) return;
		loading = true;
		try {
			revealedCreds = await api.revealWizardCredentials('tgwebproxy', targetId);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка получения ключей');
		} finally {
			loading = false;
		}
	}

	const canNext = $derived.by(() => {
		if (loading || capsError != null || !caps) return false;
		if (currentStep === 0) return true;
		if (currentStep === 1) {
			if (scenario !== 'direct_fake_tls' && !publicDomain.trim()) return false;
			if (scenario !== 'cdn_http' && !directHost.trim()) return false;
			return true;
		}
		if (currentStep === 2) return preflight?.can_proceed ?? false;
		if (currentStep === 3) return planRecord != null;
		if (currentStep === 4) return currentJob?.phase === 'succeeded';
		return true;
	});

	const nextLabel = $derived.by(() => {
		if (currentStep === 2) return 'Показать план';
		if (currentStep === 3) return 'Применить настройки';
		if (currentStep === 4) return 'К подключению';
		if (currentStep === 5) return 'Завершить';
		return 'Далее';
	});
</script>

<WizardShell
	bind:open
	title="Мастер настройки Telegram Web Proxy"
	subtitle="Пошаговая настройка прямого MTProxy и CDN Web-моста для Telegram"
	{steps}
	currentStepIndex={currentStep}
	{canNext}
	canBack={currentStep > 0 && currentStep < 4}
	{nextLabel}
	{loading}
	hideFooter={currentStep === 5}
	onnext={currentStep === 5 ? onclose : goNext}
	onback={goBack}
	{onclose}
>
	{#if currentStep === 0}
		<!-- Step 0: Scenario & Upstream -->
		<div class="wizard-step-content">
			{#if capsError}
				<div class="caps-error-banner">
					<div class="caps-error-icon">⚠️</div>
					<div class="caps-error-body">
						<div class="caps-error-title">Ошибка загрузки параметров мастера</div>
						<div class="caps-error-msg">{capsError}</div>
					</div>
					<button type="button" class="btn-retry" onclick={() => void initWizard()}>
						Повторить
					</button>
				</div>
			{/if}

			<div>
				<div class="field-section-title">Выберите сценарий работы:</div>
				<div class="device-grid">
					<!-- Dual -->
					<button
						type="button"
						class="device-card"
						class:active={scenario === 'dual'}
						onclick={() => (scenario = 'dual')}
					>
						<div class="card-header-row">
							<div class="device-icon">
								<Zap size={20} />
							</div>
							<span class="badge-recommended">
								Рекомендуется
							</span>
						</div>
						<div class="device-title">Двойной режим</div>
						<div class="device-desc">
							Прямой MTProxy для быстрой связи + CDN Web Proxy для обхода жестких блокировок.
						</div>
					</button>

					<!-- Direct Fake-TLS -->
					<button
						type="button"
						class="device-card"
						class:active={scenario === 'direct_fake_tls'}
						onclick={() => (scenario = 'direct_fake_tls')}
					>
						<div class="card-header-row">
							<div class="device-icon">
								<Shield size={20} />
							</div>
						</div>
						<div class="device-title">Только MTProxy</div>
						<div class="device-desc">
							Прямое подключение к роутеру через Fake-TLS на порту 8443 без промежуточных CDN.
						</div>
					</button>

					<!-- CDN Web Proxy -->
					<button
						type="button"
						class="device-card"
						class:active={scenario === 'cdn_http'}
						onclick={() => (scenario = 'cdn_http')}
					>
						<div class="card-header-row">
							<div class="device-icon">
								<Globe size={20} />
							</div>
						</div>
						<div class="device-title">Только через CDN</div>
						<div class="device-desc">
							Трафик маскируется под обычный HTTP GET веб-запрос через Cloudflare CDN.
						</div>
					</button>
				</div>
			</div>

			<!-- Egress selection -->
			<div class="form-group divider-top">
				<label for="tg-egress" class="field-label">
					Интерфейс выхода Telegram в интернет (Egress):
				</label>
				<select
					id="tg-egress"
					bind:value={upstreamDevice}
					class="field-select"
				>
					{#if caps?.egress_options}
						{#each caps.egress_options as opt}
							<option value={opt.id}>
								{opt.name} {opt.available ? '' : '(недоступен)'}
							</option>
						{/each}
					{:else}
						<option value="direct">Прямой выход (WAN)</option>
					{/if}
				</select>
				<p class="field-hint">
					Выберите туннель (например, Wireguard/AWG) для обхода замедлений дата-центров Telegram провайдером.
				</p>
			</div>
		</div>

	{:else if currentStep === 1}
		<!-- Step 1: Domain & CDN Profile -->
		<div class="wizard-step-content">
			{#if scenario !== 'direct_fake_tls'}
				<div class="form-group">
					<label for="tg-public-domain" class="field-label">
						Публичный домен CDN ингресса <span class="required">*</span>:
					</label>
					<input
						id="tg-public-domain"
						bind:value={publicDomain}
						placeholder="cdn.example.com"
						class="field-input mono"
					/>
					<p class="field-hint">
						Ваш домен, направленный через Cloudflare или другой CDN на белый адрес или DDNS роутера.
					</p>
				</div>

				<div class="form-group">
					<label for="tg-cdn-profile" class="field-label">Профиль совместимости CDN:</label>
					<select
						id="tg-cdn-profile"
						bind:value={cdnProfileId}
						class="field-select"
					>
						{#if caps?.profiles}
							{#each caps.profiles as p}
								<option value={p.id}>
									{p.name} {p.recommended ? '(Рекомендуется)' : ''}
								</option>
							{/each}
						{/if}
					</select>
				</div>
			{/if}

			{#if scenario !== 'cdn_http'}
				<div class="divider-top">
					<div class="form-row-2">
						<div class="form-group">
							<label for="tg-direct-host" class="field-label">
								Прямой хост роутера (DDNS / IP) <span class="required">*</span>:
							</label>
							<input
								id="tg-direct-host"
								bind:value={directHost}
								placeholder="router.example.com или 198.51.100.1"
								class="field-input mono"
							/>
						</div>
						<div class="form-group">
							<label for="tg-direct-port" class="field-label">Порт MTProxy Fake-TLS:</label>
							<input
								id="tg-direct-port"
								type="number"
								bind:value={directPort}
								class="field-input mono"
							/>
						</div>
					</div>
					<div class="form-group mt-3">
						<label for="tg-tls-domain" class="field-label">
							SNI маскировки Fake-TLS (необязательно):
						</label>
						<input
							id="tg-tls-domain"
							bind:value={tlsDomain}
							placeholder="gateway.icloud.com"
							class="field-input mono"
						/>
						<p class="field-hint">
							Домен для имитации TLS handshake в MTProxy. Оставьте пустым для использования нейтрального по умолчанию.
						</p>
					</div>
				</div>
			{/if}
		</div>

	{:else if currentStep === 2}
		<!-- Step 2: Preflight Checks -->
		<div class="wizard-step-content">
			<p class="step-intro-text">
				Выполняются предварительные проверки системных пакетов, свободных портов и сетевых интерфейсов:
			</p>
			{#if preflight}
				<PreflightList checks={preflight.checks} />
			{/if}
		</div>

	{:else if currentStep === 3}
		<!-- Step 3: Plan Preview -->
		<div class="wizard-step-content">
			<p class="step-intro-text">
				Ознакомьтесь с планом изменений перед их атомарным применением координатором:
			</p>
			{#if planRecord?.plan}
				<PlanSummary plan={planRecord.plan} />
			{/if}
		</div>

	{:else if currentStep === 4}
		<!-- Step 4: Applying Progress -->
		{#if currentJob}
			<ApplyProgress job={currentJob} {cancelling} oncancel={cancelApply} />
		{/if}

	{:else if currentStep === 5}
		<!-- Step 5: Connection Result -->
		<div class="wizard-step-content">
			<ConnectionResult
				kind="tgwebproxy"
				creds={revealedCreds}
				{loading}
				onreveal={() => void revealSecrets()}
			/>
			<div class="finish-bar">
				<button
					type="button"
					class="btn-finish"
					onclick={onclose}
				>
					Завершить работу мастера
				</button>
			</div>
		</div>
	{/if}
</WizardShell>

<style>
	.wizard-step-content {
		display: flex;
		flex-direction: column;
		gap: 1.25rem;
	}

	.field-section-title {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
		margin-bottom: 0.625rem;
	}

	.step-intro-text {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin: 0 0 0.5rem;
		line-height: 1.4;
	}

	.device-grid {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.75rem;
	}

	@media (max-width: 640px) {
		.device-grid {
			grid-template-columns: 1fr;
		}
	}

	.device-card {
		display: flex;
		flex-direction: column;
		padding: 1rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}

	.device-card:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-border-hover);
	}

	.device-card.active {
		background: var(--color-accent-tint);
		border-color: var(--color-accent);
		box-shadow: 0 0 0 1px var(--color-accent);
	}

	.card-header-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 0.75rem;
	}

	.badge-recommended {
		font-size: 0.625rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		padding: 2px 6px;
		border-radius: var(--radius-pill);
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.device-icon {
		width: 36px;
		height: 36px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		display: flex;
		align-items: center;
		justify-content: center;
		transition: all var(--t-fast) ease;
	}

	.device-card.active .device-icon {
		background: var(--color-accent);
		color: #ffffff;
	}

	.device-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
		line-height: 1.3;
	}

	.device-desc {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		margin-top: 0.375rem;
		line-height: 1.4;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.form-row-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.875rem;
	}

	@media (max-width: 640px) {
		.form-row-2 {
			grid-template-columns: 1fr;
		}
	}

	.divider-top {
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
	}

	.mt-3 {
		margin-top: 0.75rem;
	}

	.field-label {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.field-label .required {
		color: var(--color-error);
		margin-left: 2px;
	}

	.field-input,
	.field-select {
		width: 100%;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
		font-family: inherit;
		font-size: 0.8125rem;
		padding: 0.5rem 0.75rem;
		line-height: 1.4;
		box-sizing: border-box;
		transition: border-color var(--t-fast) ease;
	}

	.field-input.mono {
		font-family: var(--font-mono);
	}

	.field-input:focus,
	.field-select:focus {
		outline: none;
		border-color: var(--color-accent);
		box-shadow: 0 0 0 2px var(--color-accent-tint);
	}

	.field-hint {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		margin: 0;
		line-height: 1.4;
	}

	.finish-bar {
		display: flex;
		justify-content: flex-end;
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
	}

	.btn-finish {
		padding: 0.5rem 1.25rem;
		border-radius: var(--radius-sm);
		background: var(--color-accent);
		color: #ffffff;
		font-size: 0.8125rem;
		font-weight: 600;
		border: none;
		cursor: pointer;
		transition: background var(--t-fast) ease;
	}

	.btn-finish:hover {
		background: var(--color-accent-hover);
	}

	.caps-error-banner {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem 1rem;
		background: rgba(239, 68, 68, 0.1);
		border: 1px solid rgba(239, 68, 68, 0.3);
		border-radius: var(--radius-sm);
		margin-bottom: 1rem;
	}

	.caps-error-icon {
		font-size: 1.25rem;
		flex-shrink: 0;
	}

	.caps-error-body {
		flex: 1;
	}

	.caps-error-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-danger, #ef4444);
		margin-bottom: 0.125rem;
	}

	.caps-error-msg {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.3;
	}

	.btn-retry {
		padding: 0.35rem 0.75rem;
		font-size: 0.75rem;
		font-weight: 500;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		cursor: pointer;
		white-space: nowrap;
		transition: background var(--t-fast) ease;
	}

	.btn-retry:hover {
		background: var(--color-bg-hover);
	}
</style>
