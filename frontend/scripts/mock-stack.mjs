// Launches the full mock stack: Prism (8080) + mock-proxy (8081) + Vite dev (5173)
// + an optional mcp-dev server (8090) so an MCP client can connect to the mock data.
// Vite is configured to proxy /api/* → http://127.0.0.1:8081 with prefix strip.
// Ctrl+C terminates all children.

import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

function hasGo() {
	return spawnSync('go', ['version'], { stdio: 'ignore' }).status === 0;
}

let mcpTempDir = null;

// `go run` does NOT forward SIGTERM to the program it compiled: killing it
// leaves mcp-dev holding :8090, and the next start's port conflict then
// takes the whole stack down. Build once, synchronously, and run the binary
// directly — clean signals and a faster restart.
function buildMcpDev() {
	mcpTempDir = mkdtempSync(join(tmpdir(), 'awgm-mcp-dev-'));
	const bin = join(mcpTempDir, 'mcp-dev');
	const res = spawnSync('go', ['build', '-o', bin, '../cmd/mcp-dev'], { stdio: ['ignore', 'inherit', 'inherit'] });
	if (res.status !== 0) {
		console.log('[mcp-dev] skipped (go build failed)');
		cleanupMcpDev();
		return null;
	}
	return bin;
}

function cleanupMcpDev() {
	if (!mcpTempDir) return;
	try {
		rmSync(mcpTempDir, { recursive: true, force: true });
	} catch {
		// best effort: a leftover temp dir is harmless
	}
	mcpTempDir = null;
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
	setTimeout(() => {
		cleanupMcpDev();
		process.exit(0);
	}, 200);
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
			const bin = buildMcpDev();
			if (bin) {
				start('mcp-dev', bin, ['--listen', '127.0.0.1:8090'], {
					MCP_DEV_KEY: process.env.MOCK_MCP_KEY ?? '',
				});
			}
		} else {
			console.log('[mcp-dev] skipped (MOCK_MCP=0 or `go` not found on PATH)');
		}
	}, 800);
}, 1500);
