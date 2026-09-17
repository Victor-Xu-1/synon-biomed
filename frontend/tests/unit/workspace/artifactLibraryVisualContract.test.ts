import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const rendererRoot = new URL('../../../packages/desktop/src/renderer/', import.meta.url);
const workspaceCss = readFileSync(new URL('pages/conversation/Workspace/workspace.css', rendererRoot), 'utf8');
const librarySource = readFileSync(
  new URL('pages/conversation/Workspace/components/SynonBiomedArtifactLibrary.tsx', rendererRoot),
  'utf8'
);

describe('artifact library compact visual contract', () => {
  it('uses responsive bounded tracks and a stable thumbnail ratio', () => {
    expect(workspaceCss).toMatch(/grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*96px\)\)/);
    expect(workspaceCss).toMatch(/@container artifact-library \(min-width:\s*320px\)[\s\S]*repeat\(3,/);
    expect(workspaceCss).toMatch(/@container artifact-library \(min-width:\s*430px\)[\s\S]*repeat\(4,/);
    expect(workspaceCss).toMatch(/aspect-ratio:\s*4\s*\/\s*3/);
    expect(workspaceCss).toMatch(/\.artifact-library__size[\s\S]*white-space:\s*nowrap/);
    expect(librarySource).not.toContain('h-92px');
  });
});
