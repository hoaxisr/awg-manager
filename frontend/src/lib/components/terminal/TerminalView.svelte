<script lang="ts">
	import '@xterm/xterm/css/xterm.css';
	import { onMount, onDestroy } from 'svelte';

	interface Props {
		onclose?: () => void;
		onerror?: (msg: string) => void;
	}

	let { onclose, onerror }: Props = $props();

	let containerEl: HTMLDivElement;
	let termInstance: any = $state(null);
	let ws: WebSocket | null = $state(null);
	let observer: ResizeObserver | null = null;
	let fitAddonRef: any = null;

	// ttyd protocol: message types are ASCII characters, not binary values!
	const TTYD_OUTPUT = '0'.charCodeAt(0);       // 0x30 server → client: terminal output
	const TTYD_SET_TITLE = '1'.charCodeAt(0);    // 0x31 server → client: set window title
	const TTYD_SET_PREFS = '2'.charCodeAt(0);    // 0x32 server → client: set preferences
	const TTYD_INPUT = '0'.charCodeAt(0);        // 0x30 client → server: terminal input
	const TTYD_RESIZE = '1'.charCodeAt(0);       // 0x31 client → server: resize {columns, rows}

	onMount(async () => {
		const [
			{ Terminal },
			{ FitAddon }
		] = await Promise.all([
			import('@xterm/xterm'),
			import('@xterm/addon-fit')
		]);

		const fitAddon = new FitAddon();
		fitAddonRef = fitAddon;

		const term = new Terminal({
			cursorBlink: true,
			fontSize: 14,
			fontFamily: 'Menlo, Monaco, "Courier New", monospace',
			theme: {
				background: '#1a1b26',
				foreground: '#c0caf5',
				cursor: '#c0caf5',
				selectionBackground: '#33467c',
				black: '#15161e',
				red: '#f7768e',
				green: '#9ece6a',
				yellow: '#e0af68',
				blue: '#7aa2f7',
				magenta: '#bb9af7',
				cyan: '#7dcfff',
				white: '#a9b1d6',
			}
		});

		term.loadAddon(fitAddon);
		term.open(containerEl);
		fitAddon.fit();

		// WebSocket connection to backend proxy.
		const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		const wsUrl = `${protocol}//${window.location.host}/api/terminal/ws`;
		const socket = new WebSocket(wsUrl);
		socket.binaryType = 'arraybuffer';

		socket.onopen = () => {
			// ttyd requires an auth message before starting the PTY.
			// Without --credential flag, empty token is accepted.
			socket.send(JSON.stringify({ AuthToken: '' }));
			// Send initial resize to ttyd.
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
					// Optional: set document title from ttyd.
					break;
				case TTYD_SET_PREFS:
					// Optional: ttyd preferences, ignore.
					break;
			}
		};

		socket.onclose = () => {
			term.writeln('\r\n\x1b[33m[Сессия завершена]\x1b[0m');
			onclose?.();
		};

		socket.onerror = () => {
			onerror?.('Не удалось подключиться к терминалу');
		};

		// Send terminal input to ttyd with protocol prefix.
		term.onData((data: string) => {
			if (socket.readyState === WebSocket.OPEN) {
				const encoder = new TextEncoder();
				const payload = encoder.encode(data);
				const msg = new Uint8Array(payload.length + 1);
				msg[0] = TTYD_INPUT;
				msg.set(payload, 1);
				socket.send(msg.buffer);
			}
		});

		// Send resize events to ttyd.
		term.onResize(({ cols, rows }: { cols: number; rows: number }) => {
			if (socket.readyState === WebSocket.OPEN) {
				sendResize(socket, cols, rows);
			}
		});

		// Handle container resize.
		observer = new ResizeObserver(() => {
			fitAddon.fit();
		});
		observer.observe(containerEl);

		termInstance = term;
		ws = socket;
	});

	function sendResize(socket: WebSocket, cols: number, rows: number) {
		const json = JSON.stringify({ columns: cols, rows: rows });
		const encoder = new TextEncoder();
		const payload = encoder.encode(json);
		const msg = new Uint8Array(payload.length + 1);
		msg[0] = TTYD_RESIZE;
		msg.set(payload, 1);
		socket.send(msg.buffer);
	}

	onDestroy(() => {
		observer?.disconnect();
		if (ws && ws.readyState === WebSocket.OPEN) {
			ws.close();
		}
		termInstance?.dispose();
	});

	// Best-effort cleanup on tab close.
	function handleBeforeUnload() {
		navigator.sendBeacon('/api/terminal/stop');
	}
</script>

<svelte:window onbeforeunload={handleBeforeUnload} />

<div class="terminal-container" bind:this={containerEl}></div>

<style>
	.terminal-container {
		width: 100%;
		height: 100%;
		background: #1a1b26;
		border-radius: 6px;
		overflow: hidden;
	}
	.terminal-container :global(.xterm) {
		padding: 8px;
		height: 100%;
	}
</style>
