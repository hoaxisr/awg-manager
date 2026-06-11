import { describe, it, expect, vi, beforeEach } from 'vitest';
import { submitMatchersOnlyEdit } from './matchersEditActions';
import { ValidationError } from './addWizardActions';

vi.mock('$lib/api/client', () => ({
  api: {
    expandGeoTag: vi.fn(),
    singboxRouterUpdateRule: vi.fn(),
    singboxRouterUpdateRuleSet: vi.fn(),
  },
}));

import { api } from '$lib/api/client';

describe('submitMatchersOnlyEdit', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('updates inline rule_set', async () => {
    await submitMatchersOnlyEdit({
      rulesList: 'example.com',
      inlineRuleSetTag: 'custom-1',
    });
    expect(api.singboxRouterUpdateRuleSet).toHaveBeenCalledWith('custom-1', {
      tag: 'custom-1',
      type: 'inline',
      rules: [{ domain_suffix: ['example.com'] }],
    });
    expect(api.singboxRouterUpdateRule).not.toHaveBeenCalled();
  });

  it('пустой список → ValidationError', async () => {
    await expect(
      submitMatchersOnlyEdit({ rulesList: '   ', inlineRuleSetTag: 'custom-1' }),
    ).rejects.toThrow(ValidationError);
  });

  it('port в списке → ValidationError', async () => {
    await expect(
      submitMatchersOnlyEdit({ rulesList: 'port:443', inlineRuleSetTag: 'custom-1' }),
    ).rejects.toThrow(/порты/i);
  });

  it('src_ip в списке → ValidationError', async () => {
    await expect(
      submitMatchersOnlyEdit({ rulesList: 'src_ip:192.168.1.1', inlineRuleSetTag: 'custom-1' }),
    ).rejects.toThrow(/source ip/i);
  });
});
