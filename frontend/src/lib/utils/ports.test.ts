import { describe, it, expect } from 'vitest';
import { parsePortEntry, parsePortsString, serializePorts } from './ports';

describe('parsePortEntry', () => {
  it('accepts "993 tcp"', () => {
    expect(parsePortEntry('993 tcp')).toEqual({ ok: true, entry: { port: 993, proto: 'TCP' } });
  });
  it('accepts forgiving forms tcp:993, 993/tcp, tcp25', () => {
    expect(parsePortEntry('tcp:993')).toEqual({ ok: true, entry: { port: 993, proto: 'TCP' } });
    expect(parsePortEntry('993/tcp')).toEqual({ ok: true, entry: { port: 993, proto: 'TCP' } });
    expect(parsePortEntry('tcp25')).toEqual({ ok: true, entry: { port: 25, proto: 'TCP' } });
  });
  it('normalizes proto to uppercase (udp)', () => {
    expect(parsePortEntry('53 udp')).toEqual({ ok: true, entry: { port: 53, proto: 'UDP' } });
  });
  it('rejects missing proto', () => {
    expect(parsePortEntry('5001')).toMatchObject({ ok: false });
  });
  it('rejects missing port', () => {
    expect(parsePortEntry('tcp')).toMatchObject({ ok: false });
  });
  it('rejects out-of-range ports', () => {
    expect(parsePortEntry('70000 tcp')).toMatchObject({ ok: false });
    expect(parsePortEntry('0 tcp')).toMatchObject({ ok: false });
  });
  it('accepts boundary ports 1 and 65535', () => {
    expect(parsePortEntry('1 tcp').ok).toBe(true);
    expect(parsePortEntry('65535 udp').ok).toBe(true);
  });
});

describe('parsePortsString / serializePorts', () => {
  it('parses backend format', () => {
    expect(parsePortsString('443 TCP, 53 UDP')).toEqual([
      { port: 443, proto: 'TCP' },
      { port: 53, proto: 'UDP' },
    ]);
  });
  it('skips invalid entries', () => {
    expect(parsePortsString('443 TCP, garbage, 53 UDP')).toEqual([
      { port: 443, proto: 'TCP' },
      { port: 53, proto: 'UDP' },
    ]);
  });
  it('dedups (case-insensitive proto)', () => {
    expect(parsePortsString('443 TCP, 443 tcp')).toEqual([{ port: 443, proto: 'TCP' }]);
  });
  it('empty/blank → empty array', () => {
    expect(parsePortsString('')).toEqual([]);
    expect(parsePortsString('   ')).toEqual([]);
  });
  it('serializes to backend format', () => {
    expect(serializePorts([{ port: 443, proto: 'TCP' }, { port: 53, proto: 'UDP' }])).toBe('443 TCP, 53 UDP');
  });
  it('round-trip parse↔serialize is stable', () => {
    expect(serializePorts(parsePortsString('443 TCP, 53 UDP'))).toBe('443 TCP, 53 UDP');
  });
  it('serialize output matches backend grammar PORT UDP|TCP', () => {
    expect(serializePorts([{ port: 1194, proto: 'UDP' }])).toMatch(/^\d+ (TCP|UDP)(, \d+ (TCP|UDP))*$/);
  });
});
