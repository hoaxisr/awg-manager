// Dumps SERVICE_PRESETS (TS const) to JSON on stdout, for the Go preset
// generator (tools/genpresets) to read the DNS-side domain data without
// hand-porting it. Run: node scripts/export-service-presets.mjs > /tmp/service-presets.json
import { SERVICE_PRESETS } from '../src/lib/data/presets.ts';

process.stdout.write(JSON.stringify(SERVICE_PRESETS, null, 2) + '\n');
