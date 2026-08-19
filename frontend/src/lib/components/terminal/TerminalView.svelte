<script lang="ts">
	import '@xterm/xterm/css/xterm.css';
	import type { Terminal } from '@xterm/xterm';
	import type { FitAddon } from '@xterm/addon-fit';
	import { onMount, onDestroy } from 'svelte';
	import { get } from 'svelte/store';
	import { theme, resolveThemeTokens } from '$lib/stores/theme';
	import { buildXtermTheme } from '$lib/utils/xterm-theme';
	import { type TerminalAutoLogin } from '$lib/utils/terminalCredentials';
	import { stripAnsi } from '$lib/utils/ansi';
	import {
		createCommandLineTracker,
		HISTORY_DEFAULT_WIDTH,
		HISTORY_MIN_WIDTH,
		loadTerminalCommands,
		loadTerminalHistoryEnabled,
		loadTerminalHistoryWidth,
		looksLikeShellPrompt,
		pushTerminalCommand,
		saveTerminalCommands,
		saveTerminalHistoryEnabled,
		saveTerminalHistoryWidth,
		SPLITTER_WIDTH,
		TERMINAL_MIN_WIDTH,
	} from '$lib/utils/terminalCommandHistory';
	import TerminalHistoryPanel from './TerminalHistoryPanel.svelte';
	import { History, KeyRound, UserRound } from 'lucide-svelte';

	interface Props {
		onclose?: () => void;
		onerror?: (msg: string) => void;
		onreconnect?: () => Promise<void>;
		autoLogin?: Pick<TerminalAutoLogin, 'login' | 'password'> | null;
		compact?: boolean;
	}

	let { onclose, onerror, onreconnect, autoLogin = null, compact = false }: Props = $props();

	let containerEl: HTMLDivElement;
	let macBodyEl: HTMLDivElement;
	let termInstance: Terminal | null = $state(null);
	let fitAddonRef: FitAddon | null = null;
	let ws: WebSocket | null = $state(null);
	let observer: ResizeObserver | null = null;
	let themeUnsub: (() => void) | null = null;
	let intentionalDisconnect = false;
	let reconnecting = $state(false);
	// Авто-восстановление (#588): бюджет ретраев покрывает худшее удержание
	// single-session слота старым прокси-хендлером (ping 30s + pong 10s) —
	// ранние 409 при реконнекте нормальны, пробуем дальше.
	const AUTO_RECONNECT_ATTEMPTS = 15;
	const AUTO_RECONNECT_DELAY_MS = 3000;
	let autoReconnecting = false;
	let lastOpenAt = 0; // время последнего успешного open
	let flapCount = 0; // подряд короткоживущие (<10с) сессии

	function sendTerminalInput(data: string) {
		if (ws?.readyState !== WebSocket.OPEN) return;
		const encoder = new TextEncoder();
		const payload = encoder.encode(data);
		const msg = new Uint8Array(payload.length + 1);
		msg[0] = TTYD_INPUT;
		msg.set(payload, 1);
		ws.send(msg.buffer);
	}

	let historyEnabled = $state(loadTerminalHistoryEnabled());
	let historyCommands = $state(loadTerminalCommands());
	let historyWidth = $state(loadTerminalHistoryWidth(HISTORY_DEFAULT_WIDTH));
	let resizingHistory = $state(false);
	let resizeRaf = 0;
	let cmdTracker = createCommandLineTracker((command) => {
		if (!historyEnabled) return;
		historyCommands = pushTerminalCommand(historyCommands, command);
		saveTerminalCommands(historyCommands);
	});

	$effect(() => {
		if (!macBodyEl || !historyEnabled) return;
		const ro = new ResizeObserver(() => {
			const bodyWidth = macBodyEl.getBoundingClientRect().width;
			const clamped = clampHistoryWidth(historyWidth, bodyWidth);
			if (clamped !== historyWidth) {
				historyWidth = clamped;
				saveTerminalHistoryWidth(clamped);
			}
			scheduleTerminalFit();
		});
		ro.observe(macBodyEl);
		return () => ro.disconnect();
	});

	// Учётные данные уходят в tty только по явному нажатию: прежний
	// авто-ввод срабатывал на ЛЮБОЙ вывод, оканчивающийся на «password:»,
	// то есть пароль роутера мог уехать в чужое приглашение (ssh, sudo,
	// mysql) внутри той же сессии.
	function sendStoredLogin() {
		if (!autoLogin?.login) return;
		sendTerminalInput(autoLogin.login + '\r');
	}

	function sendStoredPassword() {
		if (!autoLogin?.password) return;
		sendTerminalInput(autoLogin.password + '\r');
	}

	function feedShellDetection(chunk: Uint8Array) {
		const text = stripAnsi(new TextDecoder().decode(chunk));
		if (looksLikeShellPrompt(text)) {
			cmdTracker.markShellReady();
		}
	}

	function setHistoryEnabled(enabled: boolean) {
		historyEnabled = enabled;
		saveTerminalHistoryEnabled(enabled);
		if (enabled) scheduleTerminalFit();
	}

	function runHistoryCommand(command: string) {
		sendTerminalInput(command + '\r');
		if (historyEnabled) {
			historyCommands = pushTerminalCommand(historyCommands, command);
			saveTerminalCommands(historyCommands);
		}
		termInstance?.focus();
	}

	function clearHistory() {
		historyCommands = [];
		saveTerminalCommands([]);
	}

	function clampHistoryWidth(width: number, bodyWidth: number): number {
		const maxWidth = Math.max(
			HISTORY_MIN_WIDTH,
			bodyWidth - TERMINAL_MIN_WIDTH - SPLITTER_WIDTH,
		);
		return Math.min(maxWidth, Math.max(HISTORY_MIN_WIDTH, width));
	}

	function scheduleTerminalFit() {
		if (resizeRaf) cancelAnimationFrame(resizeRaf);
		resizeRaf = requestAnimationFrame(() => {
			resizeRaf = 0;
			fitAddonRef?.fit();
		});
	}

	function startHistoryResize(event: PointerEvent) {
		if (!macBodyEl) return;
		event.preventDefault();
		resizingHistory = true;
		const startX = event.clientX;
		const startWidth = historyWidth;
		const bodyWidth = macBodyEl.getBoundingClientRect().width;

		const handleMove = (moveEvent: PointerEvent) => {
			const delta = startX - moveEvent.clientX;
			historyWidth = clampHistoryWidth(startWidth + delta, bodyWidth);
			scheduleTerminalFit();
		};

		const handleUp = () => {
			resizingHistory = false;
			saveTerminalHistoryWidth(historyWidth);
			scheduleTerminalFit();
			window.removeEventListener('pointermove', handleMove);
			window.removeEventListener('pointerup', handleUp);
		};

		window.addEventListener('pointermove', handleMove);
		window.addEventListener('pointerup', handleUp);
	}

	// ttyd protocol: message types are ASCII characters, not binary values!
	const TTYD_OUTPUT = '0'.charCodeAt(0);
	const TTYD_SET_TITLE = '1'.charCodeAt(0);
	const TTYD_SET_PREFS = '2'.charCodeAt(0);
	const TTYD_INPUT = '0'.charCodeAt(0);
	const TTYD_RESIZE = '1'.charCodeAt(0);

	function sendResize(socket: WebSocket, cols: number, rows: number) {
		const json = JSON.stringify({ columns: cols, rows: rows });
		const encoder = new TextEncoder();
		const payload = encoder.encode(json);
		const msg = new Uint8Array(payload.length + 1);
		msg[0] = TTYD_RESIZE;
		msg.set(payload, 1);
		socket.send(msg.buffer);
	}

	function attachSocketHandlers(socket: WebSocket, term: Terminal, fitAddon: FitAddon) {
		socket.onopen = () => {
			lastOpenAt = Date.now();
			cmdTracker.reset();
			socket.send(JSON.stringify({ AuthToken: '' }));
			sendResize(socket, term.cols, term.rows);
			fitAddon.fit();
		};

		socket.onmessage = (ev: MessageEvent) => {
			const data = new Uint8Array(ev.data as ArrayBuffer);
			if (data.length < 1) return;

			const msgType = data[0];
			const payload = data.slice(1);

			switch (msgType) {
				case TTYD_OUTPUT:
					feedShellDetection(payload);
					term.write(payload);
					break;
				case TTYD_SET_TITLE:
					break;
				case TTYD_SET_PREFS:
					break;
			}
		};

		socket.onclose = () => {
			ws = null;
			if (intentionalDisconnect || autoReconnecting) return;
			// Неумышленный обрыв (сон вкладки, обрыв прокси): ttyd жив (без
			// --once) — переподключаемся сами, НЕ убивая сессию (#588).
			// Флап-гард: 3 подряд короткоживущие (<10с) сессии → сдаёмся в
			// error-состояние страницы (кнопка «Повторить»), ttyd не стопаем.
			if (Date.now() - lastOpenAt < 10_000) flapCount++;
			else flapCount = 0;
			if (flapCount >= 3) {
				term.writeln('\r\n\x1b[33m[Сессия завершена — соединение постоянно рвётся]\x1b[0m');
				onerror?.('Соединение с терминалом постоянно рвётся');
				return;
			}
			term.writeln('\r\n\x1b[33m[Соединение потеряно — переподключение...]\x1b[0m');
			void autoReconnect(term, fitAddon);
		};

		// НЕ вешаем page-level onerror на established-сокет: у «грязного»
		// обрыва (1006) error прилетает ПЕРЕД close — уход в error-страницу
		// размонтировал бы компонент и сорвал авто-reconnect (ревью #588).
		// Ошибка первичного коннекта репортится через reject connectSocket.
	}

	function connectSocket(term: Terminal, fitAddon: FitAddon): Promise<WebSocket> {
		const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		const wsUrl = `${protocol}//${window.location.host}/api/terminal/ws`;

		return new Promise((resolve, reject) => {
			const socket = new WebSocket(wsUrl);
			socket.binaryType = 'arraybuffer';
			attachSocketHandlers(socket, term, fitAddon);

			const priorOnOpen = socket.onopen;
			socket.onopen = (ev) => {
				priorOnOpen?.call(socket, ev);
				resolve(socket);
			};

			const priorOnError = socket.onerror;
			socket.onerror = (ev) => {
				priorOnError?.call(socket, ev);
				reject(new Error('WebSocket error'));
			};
		});
	}

	function clearScreen() {
		if (!termInstance) return;
		termInstance.clear();
		sendTerminalInput('\x0c');
		termInstance.focus();
	}

	function disconnectSession() {
		intentionalDisconnect = true;
		ws?.close();
		ws = null;
		onclose?.();
	}

	// Авто-восстановление после неумышленного обрыва: ретраи с паузой, пока
	// старый прокси-хендлер не освободит single-session слот (409) и/или ttyd
	// не поднимется. Scrollback сохраняем (в отличие от ручного reconnect).
	// Сдаёмся → onerror (error-страница с «Повторить»), ttyd НЕ стопаем.
	async function autoReconnect(term: Terminal, fitAddon: FitAddon) {
		if (autoReconnecting || intentionalDisconnect) return;
		autoReconnecting = true;
		reconnecting = true;
		try {
			for (let attempt = 0; attempt < AUTO_RECONNECT_ATTEMPTS; attempt++) {
				if (intentionalDisconnect) return;
				try {
					await onreconnect?.(); // terminalStart — идемпотентен
					const socket = await connectSocket(term, fitAddon);
					if (intentionalDisconnect) {
						// Страницу покинули, пока коннект был в полёте.
						socket.close();
						return;
					}
					ws = socket;
					term.writeln('\x1b[33m[Переподключено — новая shell-сессия]\x1b[0m');
					return;
				} catch {
					// 409 (слот ещё занят старой сессией) или ttyd поднимается
				}
				await new Promise((r) => setTimeout(r, AUTO_RECONNECT_DELAY_MS));
			}
			onerror?.('Не удалось переподключиться к терминалу');
		} finally {
			autoReconnecting = false;
			reconnecting = false;
		}
	}

	async function reconnectSession() {
		if (!termInstance || !fitAddonRef || reconnecting) return;

		reconnecting = true;
		intentionalDisconnect = true;
		ws?.close();
		ws = null;

		try {
			await onreconnect?.();
			intentionalDisconnect = false;
			termInstance.clear();
			ws = await connectSocket(termInstance, fitAddonRef);
		} catch {
			onerror?.('Не удалось переподключиться');
		} finally {
			reconnecting = false;
		}
	}

	onMount(async () => {
		const [{ Terminal }, { FitAddon }] = await Promise.all([
			import('@xterm/xterm'),
			import('@xterm/addon-fit'),
		]);

		const fitAddon = new FitAddon();
		fitAddonRef = fitAddon;

		const monoStack =
			getComputedStyle(document.documentElement).getPropertyValue('--font-mono').trim() ||
			'Menlo, Monaco, "Courier New", monospace';

		const applyXtermTheme = (term: InstanceType<typeof Terminal>) => {
			term.options.theme = buildXtermTheme(resolveThemeTokens(get(theme)));
		};

		const term = new Terminal({
			cursorBlink: true,
			fontSize: 14,
			fontFamily: monoStack,
			theme: buildXtermTheme(resolveThemeTokens(get(theme))),
		});

		themeUnsub = theme.subscribe(() => applyXtermTheme(term));

		term.loadAddon(fitAddon);
		term.open(containerEl);
		fitAddon.fit();

		term.onData((data: string) => {
			cmdTracker.feed(data);
			sendTerminalInput(data);
		});

		term.onResize(({ cols, rows }: { cols: number; rows: number }) => {
			if (ws?.readyState === WebSocket.OPEN) {
				sendResize(ws, cols, rows);
			}
		});

		intentionalDisconnect = false;
		try {
			ws = await connectSocket(term, fitAddon);
		} catch {
			onerror?.('Не удалось подключиться к терминалу');
		}

		observer = new ResizeObserver(() => {
			fitAddon.fit();
		});
		observer.observe(containerEl);

		termInstance = term;
	});

	onDestroy(() => {
		themeUnsub?.();
		observer?.disconnect();
		intentionalDisconnect = true;
		ws?.close();
		termInstance?.dispose();
	});

	function handleBeforeUnload() {
		navigator.sendBeacon('/api/terminal/stop');
	}
</script>

<svelte:window onbeforeunload={handleBeforeUnload} />

<div class="mac-window">
	<div class="mac-titlebar">
		<div class="mac-traffic-lights">
			<button
				type="button"
				class="mac-light mac-light-close"
				aria-label="Отключиться"
				disabled={reconnecting}
				onclick={disconnectSession}
			>
				<span class="mac-light-icon" aria-hidden="true">×</span>
				<span class="mac-light-tooltip">Отключиться</span>
			</button>
			<button
				type="button"
				class="mac-light mac-light-minimize"
				aria-label="Очистить экран"
				disabled={reconnecting}
				onclick={clearScreen}
			>
				<span class="mac-light-icon mac-light-icon-clear" aria-hidden="true">−</span>
				<span class="mac-light-tooltip">Очистить экран</span>
			</button>
			<button
				type="button"
				class="mac-light mac-light-maximize"
				aria-label="Переподключиться"
				disabled={reconnecting}
				onclick={reconnectSession}
			>
				<span class="mac-light-icon mac-light-icon-reconnect" aria-hidden="true">↻</span>
				<span class="mac-light-tooltip">Переподключиться</span>
			</button>
		</div>
		<span class="mac-title">Терминал</span>
		<div class="mac-titlebar-actions">
			{#if autoLogin?.login}
				<button
					type="button"
					class="history-toggle"
					title="Отправить сохранённый логин в текущее приглашение"
					onclick={sendStoredLogin}
				>
					<UserRound size={14} />
					<span>Логин</span>
				</button>
			{/if}
			{#if autoLogin?.password}
				<button
					type="button"
					class="history-toggle"
					title="Отправить сохранённый пароль в текущее приглашение"
					onclick={sendStoredPassword}
				>
					<KeyRound size={14} />
					<span>Пароль</span>
				</button>
			{/if}
			{#if !historyEnabled}
				<button
					type="button"
					class="history-toggle"
					title="Показать историю команд"
					onclick={() => setHistoryEnabled(true)}
				>
					<History size={14} />
					<span>История</span>
				</button>
			{/if}
		</div>
	</div>
	<div class="mac-body" bind:this={macBodyEl} class:resizing={resizingHistory}>
		<div class="terminal-container" bind:this={containerEl}></div>
		{#if historyEnabled}
			<button
				type="button"
				class="history-splitter"
				aria-label="Изменить ширину панели истории"
				onpointerdown={startHistoryResize}
			></button>
			<div class="history-wrap" style:width="{historyWidth}px">
				<TerminalHistoryPanel
					{compact}
					commands={historyCommands}
					onselect={runHistoryCommand}
					onclear={clearHistory}
					onclose={() => setHistoryEnabled(false)}
				/>
			</div>
		{/if}
	</div>
</div>

<style>
	.mac-window {
		display: flex;
		flex-direction: column;
		width: 100%;
		height: 100%;
		border-radius: 10px;
		overflow: hidden;
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		box-shadow:
			0 0 0 1px color-mix(in srgb, var(--color-border) 40%, transparent),
			0 12px 28px color-mix(in srgb, #000 22%, transparent),
			0 2px 8px color-mix(in srgb, #000 12%, transparent);
		box-sizing: border-box;
	}

	.mac-titlebar {
		display: grid;
		grid-template-columns: 1fr auto 1fr;
		align-items: center;
		flex-shrink: 0;
		height: 2.25rem;
		padding: 0 0.75rem;
		background: color-mix(in srgb, var(--color-bg-secondary) 88%, var(--color-bg-primary));
		border-bottom: 1px solid var(--color-border);
		user-select: none;
	}

	.mac-traffic-lights {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		grid-column: 1;
	}

	.mac-light {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 12px;
		height: 12px;
		border-radius: 50%;
		border: none;
		padding: 0;
		box-shadow: inset 0 0 0 1px color-mix(in srgb, #000 12%, transparent);
		flex-shrink: 0;
	}

	button.mac-light {
		cursor: pointer;
	}

	button.mac-light:disabled {
		cursor: wait;
		opacity: 0.75;
	}

	.mac-light-close {
		background: #ff5f57;
	}

	.mac-light-minimize {
		background: #febc2e;
	}

	.mac-light-maximize {
		background: #28c840;
	}

	.mac-light-icon {
		font-family: -apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Segoe UI', sans-serif;
		font-size: 9px;
		font-weight: 700;
		line-height: 1;
		color: color-mix(in srgb, #4a0400 72%, #000);
		opacity: 0;
		transform: scale(0.85);
		transition:
			opacity var(--t-fast, 150ms) ease,
			transform var(--t-fast, 150ms) ease;
		pointer-events: none;
	}

	.mac-light-maximize .mac-light-icon {
		color: color-mix(in srgb, #003a08 72%, #000);
	}

	.mac-light-minimize .mac-light-icon {
		color: color-mix(in srgb, #5a4300 72%, #000);
	}

	.mac-light-icon-reconnect {
		font-size: 8px;
		margin-top: -0.5px;
	}

	.mac-light-icon-clear {
		font-size: 10px;
		font-weight: 800;
		margin-top: -1px;
	}

	button.mac-light:hover .mac-light-icon,
	button.mac-light:focus-visible .mac-light-icon {
		opacity: 1;
		transform: scale(1);
	}

	.mac-light-tooltip {
		position: absolute;
		top: calc(100% + 6px);
		left: 0;
		transform: translateY(2px);
		padding: 0.2rem 0.45rem;
		border-radius: 4px;
		background: color-mix(in srgb, var(--color-bg-tertiary) 92%, #000);
		border: 1px solid var(--color-border);
		box-shadow: 0 4px 12px color-mix(in srgb, #000 18%, transparent);
		color: var(--color-text-primary);
		font-family: -apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Segoe UI', sans-serif;
		font-size: 0.6875rem;
		font-weight: 500;
		line-height: 1.2;
		white-space: nowrap;
		opacity: 0;
		pointer-events: none;
		transition:
			opacity var(--t-fast, 150ms) ease,
			transform var(--t-fast, 150ms) ease;
		z-index: 2;
	}

	button.mac-light:hover .mac-light-tooltip,
	button.mac-light:focus-visible .mac-light-tooltip {
		opacity: 1;
		transform: translateY(0);
	}

	.mac-titlebar-actions {
		grid-column: 3;
		display: flex;
		justify-content: flex-end;
		align-items: center;
		gap: 0.35rem;
	}

	.history-toggle {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		background: color-mix(in srgb, var(--color-bg-primary) 50%, transparent);
		color: var(--color-text-muted);
		font-size: 0.72rem;
		padding: 0.15rem 0.45rem;
		cursor: pointer;
	}

	.history-toggle:hover {
		color: var(--color-text-primary);
		background: color-mix(in srgb, var(--color-bg-primary) 75%, transparent);
	}

	.mac-title {
		grid-column: 2;
		font-family: -apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Segoe UI', sans-serif;
		font-size: 0.8125rem;
		font-weight: 500;
		letter-spacing: 0.01em;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: min(24rem, 50vw);
	}

	.mac-body {
		flex: 1;
		min-height: 0;
		display: flex;
		background: var(--color-bg-primary);
	}

	.mac-body.resizing {
		cursor: col-resize;
		user-select: none;
	}

	.terminal-container {
		flex: 1;
		min-width: 160px;
		height: 100%;
		background: var(--color-bg-primary);
		overflow: hidden;
		box-sizing: border-box;
	}

	.history-splitter {
		flex-shrink: 0;
		width: 6px;
		margin: 0;
		padding: 0;
		border: none;
		background: transparent;
		cursor: col-resize;
		position: relative;
	}

	.history-splitter::after {
		content: '';
		position: absolute;
		top: 0;
		bottom: 0;
		left: 50%;
		width: 1px;
		transform: translateX(-50%);
		background: var(--color-border);
		transition: background var(--t-fast, 150ms) ease;
	}

	.history-splitter:hover::after,
	.mac-body.resizing .history-splitter::after {
		width: 2px;
		background: color-mix(in srgb, var(--accent-primary, #3b82f6) 65%, var(--color-border));
	}

	.history-wrap {
		flex-shrink: 0;
		min-width: 100px;
		height: 100%;
		border-left: 1px solid var(--color-border);
		overflow: hidden;
	}

	.terminal-container :global(.xterm) {
		height: 100%;
		padding: 0.375rem 0.5rem;
		box-sizing: border-box;
	}

	.terminal-container :global(.xterm-viewport) {
		background-color: var(--color-bg-primary) !important;
	}

	.terminal-container :global(.xterm-viewport::-webkit-scrollbar) {
		width: 8px;
	}

	.terminal-container :global(.xterm-viewport::-webkit-scrollbar-track) {
		background: transparent;
	}

	.terminal-container :global(.xterm-viewport::-webkit-scrollbar-thumb) {
		background: var(--color-border);
		border-radius: 4px;
	}

	.terminal-container :global(.xterm-viewport::-webkit-scrollbar-thumb:hover) {
		background: var(--color-border-hover);
	}

	:global([data-theme='light']) .mac-window {
		box-shadow:
			0 0 0 1px color-mix(in srgb, var(--color-border) 55%, transparent),
			0 16px 40px color-mix(in srgb, #000 10%, transparent),
			0 2px 6px color-mix(in srgb, #000 6%, transparent);
	}
</style>
