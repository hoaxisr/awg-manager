// Launches the full mock stack: Prism (8080) + mock-proxy (8081) + Vite dev (5173)
// + an optional mcp-dev server (8090) so an MCP client can connect to the mock data.
// Vite is configured to proxy /api/* → http://127.0.0.1:8081 with prefix strip.
// Ctrl+C terminates all children.

import { spawn, spawnSync } from 'node:child_process';

function hasGo() {
	return spawnSync('go', ['version'], { stdio: 'ignore' }).status === 0;
}

const children = [];

function start(name, cmd, args, env = {}) {
	const child = spawn(cmd, args, {
		stdio: ['ignore', 'inherit', 'inherit'],
		env: { ...process.env, ...env },
	});
	child.on('exit', (code, signal) => {
		console.log(`[${name}] exited (code=${code} signal=${signal})`);
		shutdown();
	});
	children.push({ name, child });
	return child;
}

function shutdown() {
	for (const { child } of children) {
		if (!child.killed) child.kill('SIGTERM');
	}
	setTimeout(() => process.exit(0), 200);
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);

start('prism', 'npx', [
	'-y', '@stoplight/prism-cli', 'mock',
	'../internal/openapi/swagger.yaml',
	'-p', '8080', '--host', '127.0.0.1',
]);

setTimeout(() => {
	start('proxy', 'node', ['scripts/mock-proxy.mjs'], { PORT: '8081', UPSTREAM: 'http://127.0.0.1:8080' });
	setTimeout(() => {
		start('vite', 'npx', ['vite', 'dev'], {
			VITE_API_TARGET: 'http://127.0.0.1:8081',
			VITE_API_STRIP_PREFIX: '1',
		});

		// MCP dev server (Go) so Claude Code / Cursor can connect to the
		// mock stack: http://127.0.0.1:8090/mcp. Skipped without a Go
		// toolchain or with MOCK_MCP=0.
		if (process.env.MOCK_MCP !== '0' && hasGo()) {
			start('mcp-dev', 'go', ['run', '../cmd/mcp-dev', '--listen', '127.0.0.1:8090'], {
				MCP_DEV_KEY: process.env.MOCK_MCP_KEY ?? '',
			});
		} else {
			console.log('[mcp-dev] skipped (MOCK_MCP=0 or `go` not found on PATH)');
		}
	}, 800);
}, 1500);
