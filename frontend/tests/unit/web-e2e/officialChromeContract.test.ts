import { describe, expect, it } from 'vitest';
import {
  assertOfficialGoogleChrome,
  requireOfficialChromeEndpoint,
  requireOfficialChromeProfileId,
} from '../../web-e2e/officialChromeContract';

const profileId = '0123456789abcdef0123456789abcdef';

describe('official Chrome acceptance contract', () => {
  it('requires an exact loopback browser WebSocket endpoint', () => {
    expect(() => requireOfficialChromeEndpoint(undefined)).toThrow('is required');
    expect(() => requireOfficialChromeEndpoint('http://127.0.0.1:9222')).toThrow('loopback browser WebSocket');
    expect(() => requireOfficialChromeEndpoint('ws://192.168.1.5:9222/devtools/browser/id')).toThrow(
      'loopback browser WebSocket'
    );
    expect(() => requireOfficialChromeEndpoint('ws://127.0.0.1:9222/devtools/page/id')).toThrow(
      'loopback browser WebSocket'
    );
    expect(requireOfficialChromeEndpoint('ws://127.0.0.1:9222/devtools/browser/id')).toBe(
      'ws://127.0.0.1:9222/devtools/browser/id'
    );
  });

  it('requires a fresh closed-form profile identifier', () => {
    expect(() => requireOfficialChromeProfileId(undefined)).toThrow('fresh 32-character');
    expect(() => requireOfficialChromeProfileId('reused-profile')).toThrow('fresh 32-character');
    expect(requireOfficialChromeProfileId(profileId.toUpperCase())).toBe(profileId);
  });

  it('accepts an isolated official Chrome executable and rejects bundled or alternate browsers', () => {
    const official = {
      brands: [] as string[],
      commandLine: [
        'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
        `--user-data-dir=C:\\Users\\Victor\\AppData\\Local\\Temp\\synon-rdkit-chrome-${profileId}`,
      ],
      product: 'Chrome/150.0.7871.182',
    };
    expect(() => assertOfficialGoogleChrome(official, profileId)).not.toThrow();
    expect(() =>
      assertOfficialGoogleChrome(
        {
          ...official,
          commandLine: ['/home/user/.cache/ms-playwright/chromium/chrome-linux/chrome', '--user-data-dir=/tmp/test'],
        },
        profileId
      )
    ).toThrow('official Google Chrome');
    expect(() => assertOfficialGoogleChrome({ ...official, brands: ['Microsoft Edge'] }, profileId)).toThrow(
      'official Google Chrome'
    );
    for (const unsafeProfileArguments of [
      ['--user-data-dir='],
      ['--user-data-dir=relative-profile'],
      ['--user-data-dir=C:\\Users\\Victor\\AppData\\Local\\Google\\Chrome\\User Data'],
      [official.commandLine[1], '--user-data-dir=C:\\Temp\\override'],
      ['--user-data-dir=C:\\Users\\Victor\\AppData\\Local\\Temp\\synon-rdkit-chrome-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'],
    ]) {
      expect(() =>
        assertOfficialGoogleChrome(
          { ...official, commandLine: [official.commandLine[0], ...unsafeProfileArguments] },
          profileId
        )
      ).toThrow('official Google Chrome');
    }
  });
});
