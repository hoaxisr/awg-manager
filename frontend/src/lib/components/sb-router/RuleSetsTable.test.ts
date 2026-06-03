import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import RuleSetsTable from './RuleSetsTable.svelte';

describe('RuleSetsTable', () => {
  it('renders dat conversion rule sets as dat source instead of raw URL', () => {
    render(RuleSetsTable, {
      props: {
        ruleSets: [
          {
            tag: 'geosite-GOOGLE',
            type: 'remote',
            format: 'binary',
            url: 'http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE&token=secret',
            update_interval: '24h',
          },
        ],
        onEdit: vi.fn(),
        onDelete: vi.fn(),
      },
    });

    expect(screen.getByText('dat')).toBeTruthy();
    expect(screen.getByText('geosite: GOOGLE')).toBeTruthy();
    expect(screen.getByText('direct')).toBeTruthy();
  });

  it('renders multi-tag dat sources with a single kind prefix', () => {
    render(RuleSetsTable, {
      props: {
        ruleSets: [
          {
            tag: 'geosite-google',
            type: 'remote',
            format: 'binary',
            url: 'http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE-PLAY&tag=GOOGLE-DEEPMIND&tag=GOOGLE-GEMINI&token=secret',
            update_interval: '24h',
          },
        ],
        onEdit: vi.fn(),
        onDelete: vi.fn(),
      },
    });

    expect(screen.getByText('geosite: GOOGLE-PLAY, GOOGLE-DEEPMIND, GOOGLE-GEMINI')).toBeTruthy();
    expect(screen.queryByText(/geosite:GOOGLE-DEEPMIND/)).toBeNull();
  });
});
