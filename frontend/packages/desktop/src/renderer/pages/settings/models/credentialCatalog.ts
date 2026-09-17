import type { TFunction } from 'i18next';
import type { SynonBiomedSecret, SynonBiomedSecretInput } from '@/renderer/services/synonBiomedWorkspaceSettings';

export type CredentialProviderId = 'generic' | 'aws' | 'github' | 'gcp' | 'literature' | 'azure' | 'modal' | 'nvidia';

export type CredentialEditorField = {
  key: string;
  labelKey: string;
  placeholder?: string;
  placeholderKey?: string;
  helperKey?: string;
  secret?: boolean;
  multiline?: boolean;
  required?: boolean;
  kind: 'name' | 'value' | 'credential' | 'region' | 'buckets';
};

export type LocalizedCredentialEditorField = CredentialEditorField & {
  label: string;
  helper?: string;
};

export type CredentialProviderDefinition = {
  id: CredentialProviderId;
  labelKey: string;
  descriptionKey: string;
  backendProvider: string;
  defaultName: string;
  credentialType: string;
  fields: CredentialEditorField[];
};

export type LocalizedCredentialProviderDefinition = Omit<CredentialProviderDefinition, 'fields'> & {
  label: string;
  description: string;
  fields: LocalizedCredentialEditorField[];
};

const NAME_FIELD: CredentialEditorField = {
  key: 'name',
  labelKey: 'fields.name.label',
  placeholderKey: 'fields.name.placeholder',
  kind: 'name',
  required: true,
};

export const CREDENTIAL_PROVIDERS: CredentialProviderDefinition[] = [
  {
    id: 'aws',
    labelKey: 'providers.aws.label',
    descriptionKey: 'providers.aws.description',
    backendProvider: 'aws',
    defaultName: 'AWS',
    credentialType: 'access_key',
    fields: [
      NAME_FIELD,
      {
        key: 'access_key_id',
        labelKey: 'fields.accessKeyId.label',
        placeholder: 'AKIA...',
        kind: 'credential',
        required: true,
      },
      {
        key: 'secret_access_key',
        labelKey: 'fields.secretAccessKey.label',
        kind: 'credential',
        secret: true,
        required: true,
      },
      { key: 'region', labelKey: 'fields.region.label', placeholder: 'us-east-1', kind: 'region' },
      {
        key: 'buckets',
        labelKey: 'fields.s3Buckets.label',
        placeholder: 'bucket-one, bucket-two',
        helperKey: 'fields.s3Buckets.helper',
        kind: 'buckets',
      },
      {
        key: 'endpoint',
        labelKey: 'fields.s3Endpoint.label',
        placeholder: 'https://s3.example.org',
        helperKey: 'providers.aws.endpointHelper',
        kind: 'credential',
      },
    ],
  },
  {
    id: 'github',
    labelKey: 'providers.github.label',
    descriptionKey: 'providers.github.description',
    backendProvider: 'github',
    defaultName: 'GitHub',
    credentialType: 'token',
    fields: [
      {
        key: 'value',
        labelKey: 'fields.personalAccessToken.label',
        placeholder: 'github_pat_...',
        kind: 'value',
        secret: true,
        required: true,
      },
    ],
  },
  {
    id: 'gcp',
    labelKey: 'providers.gcp.label',
    descriptionKey: 'providers.gcp.description',
    backendProvider: 'gcp',
    defaultName: 'Google Cloud',
    credentialType: 'service_account',
    fields: [
      NAME_FIELD,
      {
        key: 'service_account_key',
        labelKey: 'fields.serviceAccountKey.label',
        placeholder: '{ "type": "service_account", ... }',
        helperKey: 'providers.gcp.serviceAccountHelper',
        kind: 'credential',
        secret: true,
        multiline: true,
        required: true,
      },
      {
        key: 'buckets',
        labelKey: 'fields.gcsBuckets.label',
        placeholder: 'bucket-one, bucket-two',
        helperKey: 'fields.gcsBuckets.helper',
        kind: 'buckets',
      },
    ],
  },
  {
    id: 'literature',
    labelKey: 'providers.literature.label',
    descriptionKey: 'providers.literature.description',
    backendProvider: 'literature',
    defaultName: 'Literature access',
    credentialType: 'publisher_keys',
    fields: [
      { key: 'elsevier_api_key', labelKey: 'fields.elsevierApiKey.label', kind: 'credential', secret: true },
      {
        key: 'elsevier_institution_token',
        labelKey: 'fields.elsevierInstitutionToken.label',
        helperKey: 'providers.literature.institutionHelper',
        kind: 'credential',
        secret: true,
      },
      { key: 'springer_api_key', labelKey: 'fields.springerApiKey.label', kind: 'credential', secret: true },
      {
        key: 'semantic_scholar_api_key',
        labelKey: 'fields.semanticScholarApiKey.label',
        kind: 'credential',
        secret: true,
      },
      {
        key: 'ncbi_api_key',
        labelKey: 'fields.ncbiApiKey.label',
        helperKey: 'providers.literature.ncbiHelper',
        kind: 'credential',
        secret: true,
      },
      { key: 'core_api_key', labelKey: 'fields.coreApiKey.label', kind: 'credential', secret: true },
      {
        key: 'ezproxy_url',
        labelKey: 'fields.ezproxyUrl.label',
        placeholder: 'https://proxy.university.edu',
        kind: 'credential',
      },
      {
        key: 'session_cookie',
        labelKey: 'fields.sessionCookie.label',
        helperKey: 'providers.literature.sessionHelper',
        kind: 'credential',
        secret: true,
      },
    ],
  },
  {
    id: 'azure',
    labelKey: 'providers.azure.label',
    descriptionKey: 'providers.azure.description',
    backendProvider: 'azure',
    defaultName: 'Azure',
    credentialType: 'client_secret',
    fields: [
      NAME_FIELD,
      { key: 'tenant_id', labelKey: 'fields.tenantId.label', kind: 'credential', required: true },
      { key: 'client_id', labelKey: 'fields.clientId.label', kind: 'credential', required: true },
      {
        key: 'client_secret',
        labelKey: 'fields.clientSecret.label',
        kind: 'credential',
        secret: true,
        required: true,
      },
      { key: 'subscription_id', labelKey: 'fields.subscriptionId.label', kind: 'credential' },
      { key: 'storage_account', labelKey: 'fields.storageAccount.label', kind: 'credential' },
      {
        key: 'buckets',
        labelKey: 'fields.blobContainers.label',
        placeholder: 'container-one, container-two',
        helperKey: 'providers.azure.containersHelper',
        kind: 'buckets',
      },
    ],
  },
  {
    id: 'modal',
    labelKey: 'providers.modal.label',
    descriptionKey: 'providers.modal.description',
    backendProvider: 'modal',
    defaultName: 'Modal',
    credentialType: 'token_pair',
    fields: [
      { key: 'token_id', labelKey: 'fields.tokenId.label', placeholder: 'ak-...', kind: 'credential', required: true },
      {
        key: 'token_secret',
        labelKey: 'fields.tokenSecret.label',
        placeholder: 'as-...',
        kind: 'credential',
        secret: true,
        required: true,
      },
    ],
  },
  {
    id: 'nvidia',
    labelKey: 'providers.nvidia.label',
    descriptionKey: 'providers.nvidia.description',
    backendProvider: 'generic',
    defaultName: 'NVIDIA_API_KEY',
    credentialType: 'api_key',
    fields: [
      {
        key: 'value',
        labelKey: 'fields.nvidiaApiKey.label',
        placeholder: 'nvapi-...',
        kind: 'value',
        secret: true,
        required: true,
      },
    ],
  },
];

export const CUSTOM_CREDENTIAL_PROVIDER: CredentialProviderDefinition = {
  id: 'generic',
  labelKey: 'providers.generic.label',
  descriptionKey: 'providers.generic.description',
  backendProvider: 'generic',
  defaultName: '',
  credentialType: 'environment_variable',
  fields: [
    {
      ...NAME_FIELD,
      placeholder: 'ENV_VAR_NAME',
      helperKey: 'providers.generic.nameHelper',
    },
    {
      key: 'value',
      labelKey: 'fields.value.label',
      placeholderKey: 'fields.value.placeholder',
      kind: 'value',
      secret: true,
      required: true,
    },
    { key: 'description', labelKey: 'fields.description.label', kind: 'credential' },
  ],
};

export function localizeCredentialProvider(
  provider: CredentialProviderDefinition,
  t: TFunction
): LocalizedCredentialProviderDefinition {
  return {
    ...provider,
    label: translateCatalogCopy(t, provider.labelKey),
    description: translateCatalogCopy(t, provider.descriptionKey),
    fields: provider.fields.map((field) => ({
      ...field,
      label: translateCatalogCopy(t, field.labelKey),
      placeholder: field.placeholderKey ? translateCatalogCopy(t, field.placeholderKey) : field.placeholder,
      helper: field.helperKey ? translateCatalogCopy(t, field.helperKey) : undefined,
    })),
  };
}

export function credentialProviderForSecret(secret: SynonBiomedSecret): CredentialProviderDefinition | null {
  if (secret.provider === 'generic' && secret.name.trim().toUpperCase() === 'NVIDIA_API_KEY') {
    return CREDENTIAL_PROVIDERS.find((provider) => provider.id === 'nvidia') ?? null;
  }
  return CREDENTIAL_PROVIDERS.find((provider) => provider.backendProvider === secret.provider) ?? null;
}

/**
 * Model API keys are owned by the model profile editor, not the general
 * credentials screen. Keep them in the shared secret vault, but avoid
 * presenting a second model-credential management surface here.
 */
export function isModelCredential(secret: SynonBiomedSecret): boolean {
  return secret.provider.trim().toLowerCase() === 'llm';
}

export function isCustomCredential(secret: SynonBiomedSecret): boolean {
  return credentialProviderForSecret(secret) === null;
}

export function credentialDisplayName(
  secret: SynonBiomedSecret,
  provider: LocalizedCredentialProviderDefinition
): string {
  const name = secret.name.trim();
  return !name || name === provider.defaultName ? provider.label : name;
}

export function initialCredentialValues(
  provider: CredentialProviderDefinition,
  secret: SynonBiomedSecret | null
): Record<string, string> {
  return Object.fromEntries(
    provider.fields.map((field) => {
      if (field.kind === 'name') return [field.key, secret?.name || provider.defaultName];
      if (field.kind === 'region') return [field.key, secret?.region ?? ''];
      if (field.kind === 'buckets') return [field.key, secret?.buckets.join(', ') ?? ''];
      if (field.key === 'description') return [field.key, secret?.description ?? ''];
      return [field.key, ''];
    })
  );
}

export function buildCredentialInput(
  provider: CredentialProviderDefinition,
  values: Record<string, string>,
  editing: boolean
): SynonBiomedSecretInput {
  const credentials: Record<string, string> = {};
  const input: SynonBiomedSecretInput = {
    provider: provider.backendProvider,
    name: values.name?.trim() || provider.defaultName,
    credentialType: provider.credentialType,
  };

  for (const field of provider.fields) {
    const value = values[field.key]?.trim() ?? '';
    if (field.kind === 'value') {
      if (value) input.value = value;
    } else if (field.kind === 'credential' && field.key !== 'description') {
      if (value) credentials[field.key] = value;
    } else if (field.kind === 'region') {
      input.region = value;
    } else if (field.kind === 'buckets') {
      input.buckets = value
        .split(',')
        .map((bucket) => bucket.trim())
        .filter(Boolean);
    } else if (field.key === 'description') {
      input.description = value;
    }
  }

  if (Object.keys(credentials).length > 0) input.credentials = credentials;
  if (editing && !input.value) delete input.value;
  if (editing && !input.credentials) delete input.credentials;
  return input;
}

export type CredentialValidationError =
  | { code: 'nameRequired' }
  | { code: 'fieldRequired'; fieldKey: string }
  | { code: 'literatureRequired' };

export function validateCredentialValues(
  provider: CredentialProviderDefinition,
  values: Record<string, string>,
  editing: boolean
): CredentialValidationError | null {
  if (!(values.name?.trim() || provider.defaultName)) {
    return { code: 'nameRequired' };
  }
  const secretFields = provider.fields.filter((field) => field.kind === 'value' || field.kind === 'credential');
  const enteredSecretField = secretFields.some((field) => values[field.key]?.trim());
  if (editing && !enteredSecretField) return null;

  const required = secretFields.filter((field) => field.required);
  const missing = required.find((field) => !values[field.key]?.trim());
  if (missing) {
    return { code: 'fieldRequired', fieldKey: missing.key };
  }
  if (provider.id === 'literature' && !enteredSecretField) {
    return { code: 'literatureRequired' };
  }
  return null;
}

export function localizeCredentialValidationError(
  error: CredentialValidationError,
  provider: LocalizedCredentialProviderDefinition,
  t: TFunction
): string {
  if (error.code === 'nameRequired') {
    return t('settings.credentialsSettings.validation.nameRequired');
  }
  if (error.code === 'literatureRequired') {
    return t('settings.credentialsSettings.validation.literatureRequired');
  }
  const field = provider.fields.find((candidate) => candidate.key === error.fieldKey);
  return t('settings.credentialsSettings.validation.fieldRequired', { field: field?.label ?? error.fieldKey });
}

function translateCatalogCopy(t: TFunction, key: string): string {
  return t(`settings.credentialsSettings.catalog.${key}`);
}
