export type OfficialChromeIdentity = {
  brands: string[];
  commandLine: string[];
  product: string;
};

const officialChromeExecutableSuffixes = [
  '/google/chrome/application/chrome.exe',
  '/opt/google/chrome/chrome',
  '/applications/google chrome.app/contents/macos/google chrome',
];

export function requireOfficialChromeProfileId(value: string | undefined): string {
  const profileId = value?.trim().toLowerCase();
  if (!profileId || !/^[a-f0-9]{32}$/.test(profileId)) {
    throw new Error('SYNON_GO_OFFICIAL_CHROME_PROFILE_ID must be a fresh 32-character hexadecimal identifier');
  }
  return profileId;
}

export function requireOfficialChromeEndpoint(value: string | undefined): string {
  const raw = value?.trim();
  if (!raw) throw new Error('SYNON_GO_OFFICIAL_CHROME_CDP is required for this acceptance test');

  let endpoint: URL;
  try {
    endpoint = new URL(raw);
  } catch {
    throw new Error('SYNON_GO_OFFICIAL_CHROME_CDP must be a valid loopback WebSocket URL');
  }
  if (
    endpoint.protocol !== 'ws:' ||
    (endpoint.hostname !== '127.0.0.1' && endpoint.hostname !== '[::1]') ||
    !endpoint.port ||
    endpoint.username ||
    endpoint.password ||
    !endpoint.pathname.startsWith('/devtools/browser/') ||
    endpoint.search ||
    endpoint.hash
  ) {
    throw new Error('SYNON_GO_OFFICIAL_CHROME_CDP must be a loopback browser WebSocket endpoint');
  }
  return endpoint.toString();
}

export function assertOfficialGoogleChrome(identity: OfficialChromeIdentity, expectedProfileId: string): void {
  const brands = identity.brands.map((brand) => brand.trim().toLowerCase()).filter(Boolean);
  const executable = (identity.commandLine[0] ?? '').replaceAll('\\', '/').toLowerCase();
  const profileArguments = identity.commandLine.filter((argument) => argument.startsWith('--user-data-dir='));
  const profilePath = (profileArguments[0] ?? '').slice('--user-data-dir='.length).replaceAll('\\', '/');
  const expectedBasename = `synon-rdkit-chrome-${expectedProfileId}`;
  const normalizedProfile = profilePath.toLowerCase();
  const isolatedTemporaryProfile =
    new RegExp(`^[a-z]:/users/[^/]+/appdata/local/temp/${expectedBasename}$`, 'i').test(profilePath) ||
    normalizedProfile === `/tmp/${expectedBasename}` ||
    new RegExp(`^/var/folders/[^/]+/[^/]+/t/${expectedBasename}$`, 'i').test(profilePath);
  if (
    !/^Chrome\/\d+\.\d+\.\d+\.\d+$/.test(identity.product) ||
    !officialChromeExecutableSuffixes.some((suffix) => executable.endsWith(suffix)) ||
    profileArguments.length !== 1 ||
    !isolatedTemporaryProfile ||
    brands.some((brand) => /edge|opera|brave/.test(brand))
  ) {
    throw new Error('RDKit acceptance requires official Google Chrome');
  }
}
