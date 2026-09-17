import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  loadSynonBiomedUserProfile,
  readSynonBiomedAvatarFile,
  saveSynonBiomedUserProfile,
  subscribeSynonBiomedUserProfile,
  SYNON_BIOMED_AVATAR_MAX_BYTES,
} from '@/renderer/services/synonBiomedUserProfile';

describe('Synon Biomed user profile preferences', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it('stores profiles per account and notifies same-tab subscribers', () => {
    const listener = vi.fn();
    const unsubscribe = subscribeSynonBiomedUserProfile('user-1', listener);

    expect(loadSynonBiomedUserProfile('user-1', 'victor')).toEqual({
      displayName: 'Victor',
      avatarDataUrl: null,
    });

    saveSynonBiomedUserProfile('user-1', {
      displayName: '  研究员 Victor  ',
      avatarDataUrl: 'data:image/png;base64,iVBORw0KGgo=',
    });

    expect(listener).toHaveBeenCalledTimes(1);
    expect(loadSynonBiomedUserProfile('user-1', 'victor')).toEqual({
      displayName: '研究员 Victor',
      avatarDataUrl: 'data:image/png;base64,iVBORw0KGgo=',
    });
    expect(loadSynonBiomedUserProfile('user-2', 'alice').displayName).toBe('Alice');
    unsubscribe();
  });

  it('rejects malformed stored avatar data', () => {
    expect(() =>
      saveSynonBiomedUserProfile('user-1', {
        displayName: 'Victor',
        avatarDataUrl: 'javascript:alert(1)',
      })
    ).toThrow('user_profile_avatar_invalid');
  });

  it('validates avatar file type and size before reading', async () => {
    await expect(readSynonBiomedAvatarFile(new File(['text'], 'avatar.txt', { type: 'text/plain' }))).rejects.toThrow(
      'user_profile_avatar_type_invalid'
    );
    await expect(
      readSynonBiomedAvatarFile(
        new File([new Uint8Array(SYNON_BIOMED_AVATAR_MAX_BYTES + 1)], 'avatar.png', { type: 'image/png' })
      )
    ).rejects.toThrow('user_profile_avatar_size_invalid');
  });
});
