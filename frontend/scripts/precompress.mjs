// Precompress build/ text assets into .gz siblings and drop the raw
// original. The Go SPA handler (internal/server/server.go) serves the .gz
// directly to gzip-capable clients (every browser) and gunzips on the fly
// for the rare non-gzip client. Runs via the "postbuild" npm lifecycle, so
// it fires after every `npm run build` regardless of caller (CI or local).
import { readdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { join, extname } from 'node:path';
import { gzipSync } from 'node:zlib';

const ROOT = 'build';
const COMPRESSIBLE = new Set(['.js', '.css', '.html', '.json', '.svg', '.webmanifest']);

let gzipped = 0;
let keptRaw = 0;

function walk(dir) {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const p = join(dir, entry.name);
		if (entry.isDirectory()) {
			walk(p);
			continue;
		}
		if (!COMPRESSIBLE.has(extname(entry.name))) continue;

		const raw = readFileSync(p);
		const gz = gzipSync(raw, { level: 9 });
		// Tiny files can grow under gzip — keep the raw original instead.
		if (gz.length >= raw.length) {
			keptRaw++;
			continue;
		}
		writeFileSync(p + '.gz', gz);
		rmSync(p);
		gzipped++;
	}
}

walk(ROOT);
console.log(`precompress: ${gzipped} gzipped, ${keptRaw} left raw (gzip not smaller)`);
