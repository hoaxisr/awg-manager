import { expect, it, vi } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import ZapretHostlistPanel from './ZapretHostlistPanel.svelte';

it('retains the draft when a conflicting save is rejected', async () => {
 const onsave = vi.fn().mockResolvedValue(false);
 const { getByRole } = render(ZapretHostlistPanel, { props: {
  info: { id: 'user', path: '/opt/zapret/ipset/zapret-hosts-user.txt', exists: true, entries: 1, domains: 1, ips: 0, editable: true, gzipped: false, generated: false },
  content: 'original.example\n', loading: false, loadError: false, saving: false, onload: vi.fn(), onsave
 }});
 const input = getByRole('textbox') as HTMLTextAreaElement;
 await fireEvent.input(input, { target: { value: 'my-draft.example\n' } });
 await fireEvent.click(getByRole('button', { name: 'Сохранить' }));
 await waitFor(() => expect(onsave).toHaveBeenCalledWith('my-draft.example\n'));
 expect(input.value).toBe('my-draft.example\n');
});
