<script lang="ts">
	import '@xterm/xterm/css/xterm.css';
	import type { Terminal } from '@xterm/xterm';
	import type { FitAddon } from '@xterm/addon-fit';
	import { onMount, onDestroy } from 'svelte';
	import { get } from 'svelte/store';
	import { theme, resolveThemeTokens } from '$lib/stores/theme';
	import { buildXtermTheme } from '$lib/utils/xterm-theme';

	interface Props {
		onclose?: () => void;
		onerror?: (msg: string) => void;
		onreconnect?: () => Promise<void>;
	}

	let { onclose, onerror, onreconnect }: Props = $props();

	let containerEl: HTMLDivElement;
	let termInstance: Terminal | null = $state(null);
	let fitAddonRef: FitAddon | null = null;
	let ws: WebSocket | null = $state(null);
	let observer: ResizeObserver | null = null;
	let themeUnsub: (() => void) | null = null;
	let intentionalDisconnect = false;
	let reconnecting = $state(false);
	let lastAutoReconnectAt = 0;

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
			if (intentionalDisconnect) return;
			// Неумышленный обрыв (сон вкладки, обрыв прокси): ttyd жив (без
			// --once) — переподключаемся сами, не убивая сессию (#588).
			// Защита от цикла: если и повторный коннект умер в течение 5с —
			// сдаёмся и завершаем сессию как раньше.
			if (Date.now() - lastAutoReconnectAt < 5000) {
				term.writeln('\r\n\x1b[33m[Сессия завершена — соединение постоянно рвётся]\x1b[0m');
				onclose?.();
				return;
			}
			lastAutoReconnectAt = Date.now();
			term.writeln('\r\n\x1b[33m[Соединение потеряно — переподключение...]\x1b[0m');
			void reconnectSession();
		};

		socket.onerror = () => {
			if (!intentionalDisconnect) {
				onerror?.('Не удалось подключиться к терминалу');
			}
		};
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

	function sendTerminalInput(data: string) {
		if (ws?.readyState !== WebSocket.OPEN) return;
		const encoder = new TextEncoder();
		const payload = encoder.encode(data);
		const msg = new Uint8Array(payload.length + 1);
		msg[0] = TTYD_INPUT;
		msg.set(payload, 1);
		ws.send(msg.buffer);
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
	</div>
	<div class="mac-body">
		<div class="terminal-container" bind:this={containerEl}></div>
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

	.terminal-container {
		width: 100%;
		height: 100%;
		background: var(--color-bg-primary);
		overflow: hidden;
		box-sizing: border-box;
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
