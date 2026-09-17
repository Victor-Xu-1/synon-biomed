import { describe, expect, it } from 'vitest';
import {
  filterComposerReferences,
  getActiveComposerReferenceQuery,
  insertSerializedComposerReference,
  serializeComposerReference,
  type ArtifactComposerReference,
  type SessionComposerReference,
} from '@/renderer/components/chat/SendBox/composerReferenceModel';

const artifact = (overrides: Partial<ArtifactComposerReference> = {}): ArtifactComposerReference => ({
  kind: 'artifact',
  key: 'artifact:a:v',
  label: 'result.csv',
  detail: '项目 A',
  projectId: 'project-a',
  projectName: '项目 A',
  isCurrentProject: true,
  artifactId: 'artifact-a',
  versionId: 'version-a',
  ...overrides,
});

const session = (overrides: Partial<SessionComposerReference> = {}): SessionComposerReference => ({
  kind: 'session',
  key: 'session:frame-a',
  label: '蛋白结构分析',
  detail: '项目 A',
  projectId: 'project-a',
  projectName: '项目 A',
  isCurrentProject: true,
  frameId: 'frame-a',
  ...overrides,
});

describe('composer reference model', () => {
  it('serializes exact v1.1 artifact and session references', () => {
    expect(serializeComposerReference(artifact())).toBe('@[result.csv](artifact-a#version-a)');
    expect(serializeComposerReference(session())).toBe('#[蛋白结构分析](frame-a)');
  });

  it('finds active triggers without reopening completed references or artifact version fragments', () => {
    expect(getActiveComposerReferenceQuery('比较 @res', 7, '@')).toMatchObject({ query: 'res', start: 3 });
    expect(getActiveComposerReferenceQuery('引用 #蛋白', 6, '#')).toMatchObject({ query: '蛋白', start: 3 });
    expect(getActiveComposerReferenceQuery('@[result.csv](artifact-a#version-a)', 1, '@')).toBeNull();
    expect(getActiveComposerReferenceQuery('artifact-a#version-a', 20, '#')).toBeNull();
  });

  it('filters by filename, project and detail while keeping current project first', () => {
    const other = artifact({
      key: 'artifact:b:v',
      label: 'result-other.csv',
      projectId: 'project-b',
      projectName: '项目 B',
      detail: '项目 B',
      isCurrentProject: false,
    });
    expect(filterComposerReferences([other, artifact()], 'result').map((item) => item.projectId)).toEqual([
      'project-a',
      'project-b',
    ]);
    expect(filterComposerReferences([other, artifact()], '项目 b')).toEqual([other]);
  });

  it('replaces the active token and preserves surrounding input', () => {
    const query = getActiveComposerReferenceQuery('比较 @res 和结果', 7, '@');
    expect(query).not.toBeNull();
    expect(
      insertSerializedComposerReference('比较 @res 和结果', query!, '@[result.csv](artifact-a#version-a)')
    ).toEqual({
      value: '比较 @[result.csv](artifact-a#version-a) 和结果',
      caret: 38,
    });
  });
});
