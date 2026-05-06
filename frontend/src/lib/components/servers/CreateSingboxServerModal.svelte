<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { SingboxInboundServer } from '$lib/types';
	import { Button, Dropdown, Input, Modal, type DropdownOption } from '$lib/components/ui';

	type Protocol = SingboxInboundServer['protocol'];

	interface Props {
		open: boolean;
		onclose: () => void;
		onCreated?: () => void;
	}

	let { open = $bindable(false), onclose, onCreated = () => {} }: Props = $props();

	const protocolOptions: DropdownOption<Protocol>[] = [
		{ value: 'vless', label: 'VLESS Reality', description: 'UUID + TLS, опционально Reality' },
		{ value: 'hysteria2', label: 'Hysteria2', description: 'Пароль + TLS + QUIC' },
		{ value: 'naive', label: 'NaiveProxy', description: 'Логин/пароль + TLS' },
	];

	let protocol = $state<Protocol>('vless');
	let tag = $state('');
	let listen = $state('0.0.0.0');
	let listenPortStr = $state('443');
	let serverName = $state('');

	let useReality = $state(true);
	let realityHandshakeServer = $state('www.cloudflare.com');
	let realityHandshakePortStr = $state('443');
	let flow = $state('');
	let hyUpMbpsStr = $state('100');
	let hyDownMbpsStr = $state('100');
	let hyObfsPassword = $state('');
	let naiveNetwork = $state<'tcp' | 'udp'>('tcp');
	let naiveQuicCc = $state('bbr');

	let advancedOpen = $state(false);
	let certPath = $state('');
	let keyPath = $state('');
	let acmeDomain = $state('');
	let acmeEmail = $state('');
	let acmeProvider = $state('letsencrypt');

	let creating = $state(false);
	let statusLoading = $state(false);
	let statusFeatures = $state<string[]>([]);

	const DRAFT_KEY = 'awg:singbox-create-draft:v1';
	const LAST_KEY = 'awg:singbox-create-last-success:v1';

	function featureEnabled(prefix: string): boolean {
		return statusFeatures.some((f) => f === prefix || f.startsWith(prefix + '_') || f.includes(prefix));
	}

	function saveDraft(): void {
		if (typeof window === 'undefined') return;
		const draft = {
			protocol,
			tag,
			listen,
			listenPortStr,
			serverName,
			useReality,
			realityHandshakeServer,
			realityHandshakePortStr,
			flow,
			hyUpMbpsStr,
			hyDownMbpsStr,
			hyObfsPassword,
			naiveNetwork,
			naiveQuicCc,
			advancedOpen,
			certPath,
			keyPath,
			acmeDomain,
			acmeEmail,
			acmeProvider,
		};
		window.localStorage.setItem(DRAFT_KEY, JSON.stringify(draft));
	}

	function restoreDraft(): void {
		if (typeof window === 'undefined') return;
		const raw = window.localStorage.getItem(DRAFT_KEY);
		if (!raw) return;
		try {
			const d = JSON.parse(raw) as Record<string, unknown>;
			protocol = (d.protocol as Protocol) || protocol;
			tag = String(d.tag ?? tag);
			listen = String(d.listen ?? listen);
			listenPortStr = String(d.listenPortStr ?? listenPortStr);
			serverName = String(d.serverName ?? serverName);
			useReality = typeof d.useReality === 'boolean' ? d.useReality : useReality;
			realityHandshakeServer = String(d.realityHandshakeServer ?? realityHandshakeServer);
			realityHandshakePortStr = String(d.realityHandshakePortStr ?? realityHandshakePortStr);
			flow = String(d.flow ?? flow);
			hyUpMbpsStr = String(d.hyUpMbpsStr ?? hyUpMbpsStr);
			hyDownMbpsStr = String(d.hyDownMbpsStr ?? hyDownMbpsStr);
			hyObfsPassword = String(d.hyObfsPassword ?? hyObfsPassword);
			naiveNetwork = d.naiveNetwork === 'udp' ? 'udp' : 'tcp';
			naiveQuicCc = String(d.naiveQuicCc ?? naiveQuicCc);
			advancedOpen = typeof d.advancedOpen === 'boolean' ? d.advancedOpen : advancedOpen;
			certPath = String(d.certPath ?? certPath);
			keyPath = String(d.keyPath ?? keyPath);
			acmeDomain = String(d.acmeDomain ?? acmeDomain);
			acmeEmail = String(d.acmeEmail ?? acmeEmail);
			acmeProvider = String(d.acmeProvider ?? acmeProvider);
		} catch {
			// ignore malformed draft
		}
	}

	function saveLastSuccessful(): void {
		if (typeof window === 'undefined') return;
		const snapshot = {
			protocol,
			listen,
			listenPortStr,
			serverName,
			useReality,
			realityHandshakeServer,
			realityHandshakePortStr,
			flow,
			hyUpMbpsStr,
			hyDownMbpsStr,
			naiveNetwork,
			naiveQuicCc,
			acmeProvider,
		};
		window.localStorage.setItem(LAST_KEY, JSON.stringify(snapshot));
	}

	function restoreLastSuccessfulIfNoDraft(): void {
		if (typeof window === 'undefined') return;
		if (window.localStorage.getItem(DRAFT_KEY)) return;
		const raw = window.localStorage.getItem(LAST_KEY);
		if (!raw) return;
		try {
			const d = JSON.parse(raw) as Record<string, unknown>;
			protocol = (d.protocol as Protocol) || protocol;
			listen = String(d.listen ?? listen);
			listenPortStr = String(d.listenPortStr ?? listenPortStr);
			serverName = String(d.serverName ?? serverName);
			useReality = typeof d.useReality === 'boolean' ? d.useReality : useReality;
			realityHandshakeServer = String(d.realityHandshakeServer ?? realityHandshakeServer);
			realityHandshakePortStr = String(d.realityHandshakePortStr ?? realityHandshakePortStr);
			flow = String(d.flow ?? flow);
			hyUpMbpsStr = String(d.hyUpMbpsStr ?? hyUpMbpsStr);
			hyDownMbpsStr = String(d.hyDownMbpsStr ?? hyDownMbpsStr);
			naiveNetwork = d.naiveNetwork === 'udp' ? 'udp' : 'tcp';
			naiveQuicCc = String(d.naiveQuicCc ?? naiveQuicCc);
			acmeProvider = String(d.acmeProvider ?? acmeProvider);
		} catch {
			// ignore malformed state
		}
	}

	$effect(() => {
		if (!open) return;
		restoreDraft();
		restoreLastSuccessfulIfNoDraft();
	});

	$effect(() => {
		if (!open) return;
		saveDraft();
	});

	$effect(() => {
		if (!open) return;
		statusLoading = true;
		api.singboxGetStatus()
			.then((s) => {
				statusFeatures = s.features ?? [];
			})
			.catch(() => {
				statusFeatures = [];
			})
			.finally(() => {
				statusLoading = false;
			});
	});

	function resetForm() {
		protocol = 'vless';
		tag = '';
		listen = '0.0.0.0';
		listenPortStr = '443';
		serverName = '';
		useReality = true;
		realityHandshakeServer = 'www.cloudflare.com';
		realityHandshakePortStr = '443';
		flow = '';
		hyUpMbpsStr = '100';
		hyDownMbpsStr = '100';
		hyObfsPassword = '';
		naiveNetwork = 'tcp';
		naiveQuicCc = 'bbr';
		advancedOpen = false;
		certPath = '';
		keyPath = '';
		acmeDomain = '';
		acmeEmail = '';
		acmeProvider = 'letsencrypt';
		if (typeof window !== 'undefined') {
			window.localStorage.removeItem(DRAFT_KEY);
		}
	}

	function hasManualTLS(): boolean {
		return !!certPath.trim() || !!keyPath.trim() || !!acmeDomain.trim() || !!acmeEmail.trim();
	}

	function validate(): string | null {
		if (!listen.trim()) return 'Укажите listen-адрес';
		const listenPort = parseInt(listenPortStr, 10);
		if (isNaN(listenPort) || listenPort < 1 || listenPort > 65535) return 'Порт должен быть 1..65535';

		if (tag.trim() && !/^[a-zA-Z0-9._-]{2,64}$/.test(tag.trim())) {
			return 'Tag: 2..64 символа, допустимы буквы/цифры/._-';
		}

		if (advancedOpen && hasManualTLS()) {
			const hasFiles = !!certPath.trim() || !!keyPath.trim();
			const hasAcme = !!acmeDomain.trim() || !!acmeEmail.trim();

			if (hasFiles && (!certPath.trim() || !keyPath.trim())) {
				return 'Для file TLS укажите и certificate path, и key path';
			}
			if (hasAcme && (!acmeDomain.trim() || !acmeEmail.trim())) {
				return 'Для ACME укажите и домен, и email';
			}
			if (hasAcme && !featureEnabled('with_acme')) {
				return 'В текущем sing-box нет поддержки ACME (нужен build tag with_acme)';
			}
		}

		if (protocol === 'vless' && useReality) {
			if (!realityHandshakeServer.trim()) return 'Для Reality укажите handshake server';
			const p = parseInt(realityHandshakePortStr, 10);
			if (isNaN(p) || p < 1 || p > 65535) return 'Reality handshake port должен быть 1..65535';
		}

		if (protocol === 'naive' && statusFeatures.length > 0) {
			const hasNaive = featureEnabled('with_naive') || featureEnabled('with_naive_inbound') || featureEnabled('naive');
			if (!hasNaive) {
				return 'Текущая сборка sing-box, похоже, без Naive inbound';
			}
		}
		if (protocol === 'hysteria2') {
			const up = parseInt(hyUpMbpsStr, 10);
			const down = parseInt(hyDownMbpsStr, 10);
			if (isNaN(up) || up < 1 || up > 100000) return 'Hysteria2 up_mbps: 1..100000';
			if (isNaN(down) || down < 1 || down > 100000) return 'Hysteria2 down_mbps: 1..100000';
		}
		if (protocol === 'naive' && naiveNetwork !== 'tcp' && naiveNetwork !== 'udp') {
			return 'Naive network должен быть tcp или udp';
		}

		return null;
	}

	function buildPayload(): unknown {
		const listenPort = parseInt(listenPortStr, 10);
		const trimmedTag = tag.trim();
		const trimmedServerName = serverName.trim();

		if (!advancedOpen || !hasManualTLS()) {
			return {
				mode: 'simple',
				protocol,
				tag: trimmedTag || undefined,
				listen: listen.trim(),
				listenPort,
				serverName: trimmedServerName || undefined,
				useReality: protocol === 'vless' ? useReality : undefined,
			};
		}

		const tls: Record<string, unknown> = { enabled: true };
		if (trimmedServerName) tls.serverName = trimmedServerName;

		if (certPath.trim() && keyPath.trim()) {
			tls.certificatePath = certPath.trim();
			tls.keyPath = keyPath.trim();
		}
		if (acmeDomain.trim() && acmeEmail.trim()) {
			tls.acme = {
				domain: acmeDomain.trim(),
				email: acmeEmail.trim(),
				provider: acmeProvider.trim() || 'letsencrypt',
			};
		}

		const payload: Record<string, unknown> = {
			mode: 'full',
			protocol,
			tag: trimmedTag || undefined,
			listen: listen.trim(),
			listenPort,
			tls,
			users: [],
			running: false,
		};

		if (protocol === 'vless') {
			payload.users = [{ name: 'user', flow: flow.trim() || undefined }];
			if (useReality) {
				payload.reality = {
					enabled: true,
					handshakeServer: realityHandshakeServer.trim(),
					handshakePort: parseInt(realityHandshakePortStr, 10),
				};
			}
		} else if (protocol === 'hysteria2') {
			payload.users = [{ name: 'user' }];
			payload.hysteria2 = {
				upMbps: parseInt(hyUpMbpsStr, 10),
				downMbps: parseInt(hyDownMbpsStr, 10),
				obfsPassword: hyObfsPassword.trim() || undefined,
				ignoreClientBandwidth: false,
			};
		} else if (protocol === 'naive') {
			payload.users = [{ username: 'user' }];
			payload.naive = {
				network: naiveNetwork,
				quicCongestionControl: naiveQuicCc.trim() || undefined,
			};
		}

		return payload;
	}

	async function createServer() {
		const err = validate();
		if (err) {
			notifications.error(err);
			return;
		}

		const payload = buildPayload();
		try {
			creating = true;
			await api.singboxValidateServer(payload as Record<string, unknown>);
			await api.singboxCreateServer(payload as Record<string, unknown>);
			notifications.success('Sing-box сервер создан');
			saveLastSuccessful();
			onCreated();
			resetForm();
			onclose();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка создания сервера');
		} finally {
			creating = false;
		}
	}
</script>

<Modal open={open} title="Создать Sing-box сервер" size="md" {onclose}>
	<div class="form">
		<div class="info-box">
			<p><strong>Быстрый старт:</strong> выберите тип, укажите SNI (если есть домен) и нажмите «Создать сервер».</p>
			<p>Ключи, UUID и пароли будут сгенерированы автоматически.</p>
			{#if statusLoading}
				<p>Проверяем возможности текущей сборки sing-box...</p>
			{:else if statusFeatures.length > 0}
				<details class="features-spoiler">
					<summary>Параметры singbox</summary>
					<p class="features">{statusFeatures.join(', ')}</p>
				</details>
			{/if}
		</div>

		<Dropdown
			label="Тип сервера"
			value={protocol}
			options={protocolOptions}
			onchange={(v) => (protocol = v)}
			fullWidth
		/>

		<div class="grid grid-2">
			<Input
				label="Tag (опционально)"
				bind:value={tag}
				placeholder="Например: vless-main"
				hint="Внутреннее имя сервера в sing-box. Можно оставить пустым — сгенерируем сами."
				fullWidth
			/>
			<Input
				label="Server Name / SNI"
				bind:value={serverName}
				placeholder="Например: vpn.example.com"
				hint="Обычно это ваш домен для TLS. Берется из DNS-записи, ведущей на ваш сервер."
				fullWidth
			/>
		</div>

		<div class="grid grid-2">
			<Input
				label="Listen адрес"
				bind:value={listen}
				placeholder="0.0.0.0"
				hint="0.0.0.0 = слушать на всех интерфейсах роутера."
				required
				fullWidth
			/>
			<Input
				label="Listen порт"
				bind:value={listenPortStr}
				placeholder="443"
				hint="443 чаще всего меньше блокируется провайдерами."
				required
				fullWidth
			/>
		</div>

		{#if protocol === 'vless'}
			<label class="checkbox-line">
				<input type="checkbox" bind:checked={useReality} />
				<span>Включить Reality</span>
			</label>
			{#if useReality}
				<div class="grid grid-2">
					<Input
						label="Reality handshake server"
						bind:value={realityHandshakeServer}
						placeholder="www.cloudflare.com"
						hint="Маскировочный TLS-хост. Обычно крупный стабильный HTTPS-домен."
						fullWidth
					/>
					<Input
						label="Reality handshake port"
						bind:value={realityHandshakePortStr}
						placeholder="443"
						hint="Обычно 443."
						fullWidth
					/>
				</div>
			{/if}
			<Input
				label="Flow (опционально)"
				bind:value={flow}
				placeholder="xtls-rprx-vision"
				hint="Обычно оставляют пустым. Нужен только если клиент/схема этого требуют."
				fullWidth
			/>
		{:else if protocol === 'hysteria2'}
			<div class="grid grid-2">
				<Input
					label="up_mbps"
					bind:value={hyUpMbpsStr}
					placeholder="100"
					hint="Оценочная исходящая скорость канала сервера в Mbps."
					fullWidth
				/>
				<Input
					label="down_mbps"
					bind:value={hyDownMbpsStr}
					placeholder="100"
					hint="Оценочная входящая скорость канала сервера в Mbps."
					fullWidth
				/>
			</div>
			<Input
				label="obfs password (опционально)"
				bind:value={hyObfsPassword}
				placeholder="salamander-secret"
				hint="Пароль обфускации QUIC-трафика (если используете obfs на клиентах)."
				fullWidth
			/>
		{:else if protocol === 'naive'}
			<div class="grid grid-2">
				<Dropdown
					label="Network"
					value={naiveNetwork}
					options={[
						{ value: 'tcp', label: 'tcp', description: 'Стандартно и совместимо' },
						{ value: 'udp', label: 'udp', description: 'Когда нужен UDP-only режим' },
					]}
					onchange={(v) => (naiveNetwork = v)}
					fullWidth
				/>
				<Input
					label="QUIC congestion control"
					bind:value={naiveQuicCc}
					placeholder="bbr"
					hint="Рекомендованный дефолт для Naive — bbr."
					fullWidth
				/>
			</div>
		{/if}

		<button type="button" class="advanced-toggle" onclick={() => (advancedOpen = !advancedOpen)}>
			{advancedOpen ? 'Скрыть расширенные настройки TLS' : 'Показать расширенные настройки TLS'}
		</button>

		{#if advancedOpen}
			<div class="advanced">
				<div class="subhead">TLS через файлы сертификата</div>
				<div class="grid grid-2">
					<Input
						label="Certificate path"
						bind:value={certPath}
						placeholder="/opt/etc/ssl/certs/fullchain.pem"
						hint="Путь к публичному сертификату на роутере."
						fullWidth
					/>
					<Input
						label="Key path"
						bind:value={keyPath}
						placeholder="/opt/etc/ssl/private/privkey.pem"
						hint="Путь к приватному ключу сертификата."
						fullWidth
					/>
				</div>

				<div class="subhead">TLS через ACME</div>
				<div class="grid grid-2">
					<Input
						label="ACME домен"
						bind:value={acmeDomain}
						placeholder="vpn.example.com"
						hint="Домен должен резолвиться на ваш внешний IP. Также нужен with_acme в сборке sing-box."
						fullWidth
					/>
					<Input
						label="ACME email"
						bind:value={acmeEmail}
						placeholder="admin@example.com"
						hint="Email для Let's Encrypt уведомлений."
						fullWidth
					/>
				</div>
				<Input
					label="ACME provider"
					bind:value={acmeProvider}
					placeholder="letsencrypt"
					hint="Обычно оставляют letsencrypt."
					fullWidth
				/>
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" onclick={onclose} disabled={creating}>Отмена</Button>
		<Button variant="primary" onclick={createServer} loading={creating}>Создать сервер</Button>
	{/snippet}
</Modal>

<style>
	.form { display: flex; flex-direction: column; gap: 0.9rem; }
	.grid { display: grid; gap: 0.75rem; }
	.grid-2 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
	.checkbox-line { display: inline-flex; align-items: center; gap: 0.5rem; font-size: 0.82rem; color: var(--color-text-secondary); }
	.info-box { padding: 0.75rem; background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); font-size: 0.86rem; color: var(--color-text-secondary); }
	.info-box p { margin: 0 0 0.3rem; }
	.info-box p:last-child { margin-bottom: 0; }
	.features { font-family: var(--font-mono, monospace); font-size: 0.78rem; opacity: 0.9; }
	.features-spoiler summary { cursor: pointer; user-select: none; font-size: 0.82rem; color: var(--color-text-secondary); }
	.features-spoiler[open] summary { margin-bottom: 0.35rem; }
	.advanced-toggle { text-align: left; background: transparent; border: 1px dashed var(--color-border); color: var(--color-text-secondary); border-radius: var(--radius-sm); padding: 0.55rem 0.7rem; cursor: pointer; }
	.advanced { border: 1px solid var(--color-border); border-radius: var(--radius-sm); padding: 0.75rem; display: flex; flex-direction: column; gap: 0.75rem; }
	.subhead { font-size: 0.82rem; font-weight: 600; color: var(--color-text-secondary); }
	@media (max-width: 768px) { .grid-2 { grid-template-columns: 1fr; } }
</style>
