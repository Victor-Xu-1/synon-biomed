import { describe, expect, it } from 'vitest';
import {
  CREDENTIAL_PROVIDERS,
  buildCredentialInput,
  credentialDisplayName,
  credentialProviderForSecret,
  isModelCredential,
  localizeCredentialProvider,
  localizeCredentialValidationError,
  validateCredentialValues,
} from '@/renderer/pages/settings/models/credentialCatalog';
import type { SynonBiomedSecret } from '@/renderer/services/synonBiomedWorkspaceSettings';
import { createTestI18n } from '../i18nTestUtils';

const secret = (overrides: Partial<SynonBiomedSecret>): SynonBiomedSecret => ({
  id: 'secret-1',
  provider: 'generic',
  name: 'API_KEY',
  description: '',
  credentialType: 'api_key',
  buckets: [],
  region: '',
  maskedPreview: 'configured',
  maskedFields: [],
  valueConfigured: true,
  credentialsConfigured: false,
  credentialFields: [],
  createdAt: '',
  updatedAt: '',
  ...overrides,
});

describe('credential catalog', () => {
  it('maps the NVIDIA environment credential without inventing an unsupported backend provider', () => {
    const provider = credentialProviderForSecret(secret({ name: 'NVIDIA_API_KEY' }));
    expect(provider?.id).toBe('nvidia');
    expect(provider?.backendProvider).toBe('generic');
  });

  it('keeps model provider secrets owned by the model settings surface', () => {
    expect(isModelCredential(secret({ provider: 'llm', name: 'GLM-5.2' }))).toBe(true);
    expect(isModelCredential(secret({ provider: 'generic', name: 'API_KEY' }))).toBe(false);
  });

  it('builds exact AWS metadata and credentials while keeping redacted values on metadata-only edits', () => {
    const aws = CREDENTIAL_PROVIDERS.find((provider) => provider.id === 'aws');
    expect(aws).toBeDefined();
    expect(
      buildCredentialInput(
        aws!,
        {
          name: 'AWS Research',
          access_key_id: 'AKIA123',
          secret_access_key: 'secret',
          region: 'us-east-1',
          buckets: 'one, two',
          endpoint: '',
        },
        false
      )
    ).toEqual({
      provider: 'aws',
      name: 'AWS Research',
      credentialType: 'access_key',
      credentials: { access_key_id: 'AKIA123', secret_access_key: 'secret' },
      region: 'us-east-1',
      buckets: ['one', 'two'],
    });
    expect(
      buildCredentialInput(
        aws!,
        {
          name: 'AWS Renamed',
          access_key_id: '',
          secret_access_key: '',
          region: 'eu-west-1',
          buckets: '',
          endpoint: '',
        },
        true
      )
    ).not.toHaveProperty('credentials');
  });

  it('requires complete multi-field replacements and at least one literature credential', () => {
    const aws = CREDENTIAL_PROVIDERS.find((provider) => provider.id === 'aws')!;
    expect(
      validateCredentialValues(
        aws,
        { name: 'AWS', access_key_id: 'AKIA123', secret_access_key: '', region: '', buckets: '', endpoint: '' },
        true
      )
    ).toEqual({ code: 'fieldRequired', fieldKey: 'secret_access_key' });
    const literature = CREDENTIAL_PROVIDERS.find((provider) => provider.id === 'literature')!;
    expect(validateCredentialValues(literature, {}, false)).toEqual({ code: 'literatureRequired' });
  });

  it('localizes provider and field copy without changing provider contracts', async () => {
    const rawAws = CREDENTIAL_PROVIDERS.find((provider) => provider.id === 'aws')!;
    const zh = await createTestI18n('zh-CN');
    const en = await createTestI18n('en-US');
    const zhAws = localizeCredentialProvider(rawAws, zh.t);
    const enAws = localizeCredentialProvider(rawAws, en.t);

    expect(zhAws).toMatchObject({ label: 'AWS', description: 'S3、Bedrock 与 AWS 服务', backendProvider: 'aws' });
    expect(zhAws.fields.find((field) => field.key === 'access_key_id')?.label).toBe('访问密钥 ID');
    expect(enAws.fields.find((field) => field.key === 'access_key_id')?.label).toBe('Access key ID');
    expect(
      localizeCredentialValidationError({ code: 'fieldRequired', fieldKey: 'secret_access_key' }, zhAws, zh.t)
    ).toBe('请填写 秘密访问密钥。');
  });

  it('uses the localized provider label for an unchanged system default name', async () => {
    const provider = CREDENTIAL_PROVIDERS.find((candidate) => candidate.id === 'literature')!;
    const zh = await createTestI18n('zh-CN');
    const localized = localizeCredentialProvider(provider, zh.t);
    expect(credentialDisplayName(secret({ provider: 'literature', name: provider.defaultName }), localized)).toBe(
      '文献访问'
    );
  });
});
