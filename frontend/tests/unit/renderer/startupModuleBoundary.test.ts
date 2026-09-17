import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const rendererRoot = path.resolve(import.meta.dirname, '../../../packages/desktop/src/renderer');

function source(relativePath: string): string {
  return readFileSync(path.join(rendererRoot, relativePath), 'utf8');
}

describe('renderer startup module boundary', () => {
  it('keeps authenticated workspace modules out of the login/auth entry graph', () => {
    const entry = source('main.tsx');
    const authenticatedWorkspace = source('components/layout/AuthenticatedWorkspace.tsx');

    for (const authenticatedImport of [
      'components/layout/Layout',
      'components/layout/Sider',
      'ConversationHistoryContext',
      'Preview/context/PreviewContext',
      'RealtimeContext',
      'conversationRoute',
    ]) {
      expect(entry).not.toContain(authenticatedImport);
      expect(authenticatedWorkspace).toContain(authenticatedImport);
    }

    const onboardingAuthority = source('services/onboardingCompletionAuthority.ts');
    expect(onboardingAuthority).not.toMatch(/^import .*onboardingService/m);
    expect(onboardingAuthority).toContain("import('@renderer/services/onboardingService')");
  });
});
