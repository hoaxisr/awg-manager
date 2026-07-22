<script lang="ts">
	import { Modal, Button, FormToggle } from '$lib/components/ui';
	import type { FreeTurnCaptchaOverview } from '$lib/types';

	interface Props {
		open: boolean;
		clientId: string | null;
		clientName?: string;
		captchaUrl: string | null;
		overview: FreeTurnCaptchaOverview | null;
		onclose: () => void;
		onSelectClient: (id: string) => void;
		captchaAutoOpen?: boolean;
		onCaptchaAutoOpenChange?: (enabled: boolean) => void;
	}

	let {
		open = $bindable(false),
		clientId,
		clientName = '',
		captchaUrl,
		overview,
		onclose,
		onSelectClient,
		captchaAutoOpen = true,
		onCaptchaAutoOpenChange
	}: Props = $props();

	const captchaBaseUrl = $derived(captchaUrl ? `${captchaUrl}` : null);

	const queuedClients = $derived(
		(overview?.clients ?? []).filter((c) => c.waiting && c.queued)
	);

	const openableClients = $derived(
		(overview?.clients ?? []).filter((c) => c.canOpen)
	);

	const activeClient = $derived(
		openableClients.find((c) => c.active) ?? openableClients[0] ?? null
	);

	const currentClient = $derived(
		(overview?.clients ?? []).find((c) => c.clientId === clientId) ?? null
	);

	const isQueued = $derived(
		queuedClients.some((c) => c.clientId === clientId)
	);

	const LOCAL_CAPTCHA_URL = 'http://127.0.0.1:8765/';

	let sessionKey = $state(0);
	let lastCaptchaSession = $state(0);
	let autoOpenedForSession = $state(0);
	let sawWaiting = $state(false);
	let localTunnelReady = $state<boolean | null>(null);
	let captchaTabOpen = $state(false);

	const sshTunnelCmd = $derived.by(() => {
		if (typeof window === 'undefined') return '';
		const host = window.location.hostname || '192.168.90.60';
		return `ssh -N -L 8765:127.0.0.1:8765 root@${host}`;
	});

	$effect(() => {
		if (!open) {
			sawWaiting = false;
			return;
		}
		if (currentClient?.waiting) sawWaiting = true;
	});

	$effect(() => {
		const session = currentClient?.captchaSession ?? 0;
		if (!open || session <= 0) return;
		if (session !== lastCaptchaSession) {
			lastCaptchaSession = session;
			sessionKey = session;
			autoOpenedForSession = 0;
		}
	});

	const captchaLoadUrl = $derived(
		captchaBaseUrl
			? `${captchaBaseUrl}${captchaBaseUrl.includes('?') ? '&' : '?'}_s=${sessionKey}`
			: null
	);

	const preferredCaptchaUrl = $derived(
		localTunnelReady ? LOCAL_CAPTCHA_URL : captchaLoadUrl
	);

	const captchaResolved = $derived(
		sawWaiting && Boolean(currentClient && !currentClient.waiting && !isQueued)
	);

	const popupWindowName = $derived(`freeturn-captcha-${clientId ?? 'default'}`);

	async function probeLocalTunnel(): Promise<boolean> {
		if (typeof window === 'undefined') return false;
		try {
			const ctrl = new AbortController();
			const timer = setTimeout(() => ctrl.abort(), 1500);
			await fetch(LOCAL_CAPTCHA_URL, { mode: 'no-cors', signal: ctrl.signal });
			clearTimeout(timer);
			return true;
		} catch {
			return false;
		}
	}

	function selectClient(id: string) {
		onSelectClient(id);
	}

	function openCaptchaTab(): boolean {
		const url = preferredCaptchaUrl;
		if (!url || typeof window === 'undefined') return false;
		// Без noopener: с ним window.open возвращает null (нельзя понять, открылась
		// ли вкладка), а страница-хелпер не может закрыть popup через window.opener.
		const w = window.open(url, localTunnelReady ? '_blank' : popupWindowName);
		captchaTabOpen = w != null;
		return captchaTabOpen;
	}

	function reloadCaptcha() {
		sessionKey += 1;
		openCaptchaTab();
	}

	function openInNewTab() {
		if (!captchaLoadUrl) return;
		window.open(captchaLoadUrl, '_blank');
	}

	function openLocalTunnelTab() {
		window.open(LOCAL_CAPTCHA_URL, '_blank', 'noopener,noreferrer');
	}

	async function copySshCommand() {
		if (!sshTunnelCmd || typeof navigator === 'undefined' || !navigator.clipboard) return;
		try {
			await navigator.clipboard.writeText(sshTunnelCmd);
		} catch {
			// ignore
		}
	}

	$effect(() => {
		if (!open) {
			localTunnelReady = null;
			captchaTabOpen = false;
			return;
		}
		let cancelled = false;
		void probeLocalTunnel().then((ok) => {
			if (!cancelled) localTunnelReady = ok;
		});
		return () => {
			cancelled = true;
		};
	});

	$effect(() => {
		if (!open || !preferredCaptchaUrl || isQueued || !captchaAutoOpen) return;
		if (localTunnelReady === null) return;
		const session = currentClient?.captchaSession ?? sessionKey;
		if (session <= 0) return;
		if (session === autoOpenedForSession) return;
		autoOpenedForSession = session;
		openCaptchaTab();
	});
</script>

<Modal
	{open}
	title="Прохождение VK-капчи"
	size="md"
	bodyLayout="default"
	closeOnBackdrop={false}
	{onclose}
>
	<div class="ft-captcha-wrap">
		{#if currentClient?.pendingStreams && currentClient.pendingStreams > 1}
			<div class="ft-captcha-queue">
				Осталось потоков с нерешённой капчей: {currentClient.pendingStreams}.
				Один gg = один поток — не закрывайте модалку, freeturn по очереди откроет новую
				сессию капчи.
				{#if currentClient.portContention}
					Часть потоков не успела занять порт 8765 — freeturn повторит auth позже (это нормально).
				{/if}
			</div>
		{:else if overview && overview.clients.some((c) => c.waiting && c.queued)}
			<div class="ft-captcha-queue">
				{#if overview.ownerName}
					Сейчас активна капча клиента «{overview.ownerName}». Остальные ждут в очереди —
					freeturn использует один порт 8765 на роутер.
				{:else}
					Несколько клиентов ждут капчу. Решайте по одному — порт 8765 общий для всех
					экземпляров freeturn.
				{/if}
			</div>
		{/if}

		{#if openableClients.length > 1}
			<div class="ft-captcha-tabs">
				{#each openableClients as c (c.clientId)}
					<button
						type="button"
						class="ft-captcha-tab"
						class:ft-captcha-tab-active={c.clientId === clientId}
						onclick={() => selectClient(c.clientId)}
					>
						{c.clientName || c.clientId}
						{#if c.active}
							<span class="ft-captcha-badge">активна</span>
						{/if}
					</button>
				{/each}
			</div>
		{/if}

		{#if isQueued}
			<p class="ft-captcha-warn">
				Этот клиент в очереди.
				{#if activeClient}
					Сначала откройте капчу для «{activeClient.clientName || activeClient.clientId}».
				{/if}
			</p>
		{:else if captchaResolved}
			<div class="ft-captcha-ok">
				Капча для текущего потока принята. Freeturn продолжит получение TURN-кредов.
				{#if currentClient?.pendingStreams && currentClient.pendingStreams > 1}
					Дождитесь следующей сессии — окно капчи откроется снова автоматически.
				{:else}
					Можно закрыть это окно.
				{/if}
			</div>
		{:else if captchaLoadUrl}
			<div class="ft-captcha-panel">
				{#if localTunnelReady}
					<div class="ft-captcha-ok">
						SSH-туннель на <code>127.0.0.1:8765</code> обнаружен — откроется прямой доступ
						(галочка, как при ручном SSH).
					</div>
				{:else}
					<p class="ft-captcha-lead">
						Галочка VK работает только с <code>127.0.0.1:8765</code> (localhost). Через LAN
						VK часто показывает вход VK ID — после входа страница должна вернуться с
						галочкой. Если зависло — используйте SSH-туннель ниже.
					</p>
				{/if}

				<div class="ft-captcha-status" class:ft-captcha-status-ok={captchaTabOpen}>
					{#if localTunnelReady === null}
						Проверяем SSH-туннель…
					{:else if captchaTabOpen}
						Вкладка капчи открыта — пройдите «Я не робот» там.
					{:else}
						Нажмите «Открыть капчу». Если вкладка не появилась — разрешите pop-up.
					{/if}
				</div>

				<div class="ft-captcha-actions">
					<Button variant="primary" size="md" onclick={() => openCaptchaTab()}>
						{localTunnelReady ? 'Открыть 127.0.0.1:8765' : 'Открыть капчу'}
					</Button>
					{#if !localTunnelReady && captchaLoadUrl}
						<Button variant="secondary" size="sm" onclick={openInNewTab}>Прокси (новая вкладка)</Button>
					{/if}
					<button type="button" class="ft-captcha-link" onclick={reloadCaptcha}>
						Новая сессия (если протухла)
					</button>
				</div>

				{#if !localTunnelReady && sshTunnelCmd}
					<div class="ft-captcha-ssh">
						<p class="ft-captcha-ssh-title">Надёжный способ (как у вас через SSH):</p>
						<code class="ft-captcha-ssh-cmd">{sshTunnelCmd}</code>
						<div class="ft-captcha-actions">
							<Button variant="secondary" size="sm" onclick={copySshCommand}>Скопировать</Button>
							<Button variant="ghost" size="sm" onclick={openLocalTunnelTab}>
								Открыть 127.0.0.1:8765
							</Button>
						</div>
						<p class="ft-captcha-ssh-hint">
							В PowerShell/CMD выполните команду, затем «Открыть 127.0.0.1:8765» или
							<code>http://127.0.0.1:8765</code> в браузере.
						</p>
					</div>
				{/if}
			</div>
		{:else}
			<p class="ft-captcha-warn">Сервер капчи не активен. Подождите или проверьте лог клиента.</p>
		{/if}

		<p class="ft-captcha-hint">
			{#if clientName}
				Клиент: {clientName}.
			{/if}
			Если видите серый квадрат с жёлтым треугольником — сессия протухла: «Новая сессия» или
			дождитесь строки <code>Triggering manual captcha fallback</code> в логе.
		</p>
	</div>

	{#snippet actions()}
		{#if onCaptchaAutoOpenChange}
			<FormToggle
				checked={captchaAutoOpen}
				onchange={onCaptchaAutoOpenChange}
				label="Авто-открытие"
				size="sm"
			/>
		{/if}
		<Button variant="ghost" size="sm" onclick={onclose}>Закрыть</Button>
	{/snippet}
</Modal>

<style>
	.ft-captcha-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.ft-captcha-queue {
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning);
		background: var(--color-warning-tint);
		font-size: 0.8125rem;
		color: var(--color-text-primary);
	}

	.ft-captcha-tabs {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.ft-captcha-tab {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.25rem 0.5rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		font-size: 0.75rem;
		cursor: pointer;
		color: var(--color-text-secondary);
	}

	.ft-captcha-tab-active {
		border-color: var(--color-accent-border);
		color: var(--color-text-primary);
		background: var(--color-bg-primary);
	}

	.ft-captcha-badge {
		font-size: 0.6875rem;
		color: var(--color-warning);
	}

	.ft-captcha-panel {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.ft-captcha-lead {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-primary);
		line-height: 1.45;
	}

	.ft-captcha-status {
		margin: 0;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.ft-captcha-status-ok {
		border-color: var(--color-accent-border);
		color: var(--color-text-primary);
	}

	.ft-captcha-ok {
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-success);
		background: color-mix(in srgb, var(--color-success) 12%, transparent);
		font-size: 0.8125rem;
		color: var(--color-text-primary);
	}

	.ft-captcha-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}

	.ft-captcha-link {
		background: none;
		border: none;
		color: var(--color-accent);
		font-size: 0.75rem;
		cursor: pointer;
		padding: 0.125rem 0.25rem;
	}

	.ft-captcha-ssh {
		padding-top: 0.5rem;
		border-top: 1px dashed var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.ft-captcha-ssh-title {
		margin: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.ft-captcha-ssh-cmd {
		display: block;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		font-size: 0.6875rem;
		word-break: break-all;
	}

	.ft-captcha-ssh-hint {
		margin: 0;
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
	}

	.ft-captcha-warn,
	.ft-captcha-hint {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0;
	}

	.ft-captcha-warn {
		color: var(--color-warning);
	}
</style>
