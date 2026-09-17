/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';
import { describe, expect, it } from 'vitest';
import enResources from '@/renderer/services/i18n/locales/en-US';

const rendererRoot = path.resolve(process.cwd(), 'packages/desktop/src/renderer');
const localeRoot = path.join(rendererRoot, 'services/i18n/locales');
const resourcePaths = collectResourcePaths(enResources);
const bilingualOrDomainDataFiles = new Set([
  'packages/desktop/src/renderer/components/settings/SettingsModal/contents/SystemModalContent/VoiceInputSection/speechSettingsUtils.ts',
  'packages/desktop/src/renderer/components/synonBiomed/runtime/approvalScopeModel.ts',
  'packages/desktop/src/renderer/components/synonBiomed/runtime/synonBiomedReviewRepair.ts',
  'packages/desktop/src/renderer/pages/conversation/Messages/components/toolStepSummaryModel.ts',
  'packages/desktop/src/renderer/pages/settings/skills/SynonBiomedSkillMarketPanel.tsx',
  'packages/desktop/src/renderer/pages/settings/skills/skillMarketplaceCopy.ts',
  'packages/desktop/src/renderer/services/skills/synonBiomedSkillCategories.ts',
  'packages/desktop/src/renderer/services/skills/synonBiomedSkillDescriptions.ts',
]);
const allowedChineseLiterals: Record<string, RegExp[]> = {
  'packages/desktop/src/renderer/pages/conversation/components/ConversationTitleMinimap/minimapUtils.ts': [
    /^[零一二三四五六七八九十]+$/u,
    /^第$/u,
  ],
  'packages/desktop/src/renderer/pages/conversation/platforms/acp/AcpSendBox.tsx': [/^认证失败$/u],
  'packages/desktop/src/renderer/services/synonBiomedCapabilities.ts': [/^通用科研助手$/u],
  'packages/desktop/src/renderer/services/synonBiomedCatalog.ts': [
    /^通过 $/u,
    /^ 接入的 Synon Biomed 专家 Agent。$/u,
    /^使用 Synon Biomed 后端专家 $/u,
    /^ 处理生物医药研究任务。$/u,
  ],
  'packages/desktop/src/renderer/theme/builtinThemes.ts': [/^Y2K电子账本 by 椰树女王$/u],
  'packages/desktop/src/renderer/utils/synonBiomed/runtime/runtimeLogo.ts': [/^默认$/u],
};
const technicalUiLiterals = [
  /^(?:Synon Biomed|SYNON-Biomed|Modal|NVIDIA BioNeMo NIM|NVIDIA_API_KEY|SMILES|conda env|chrome-devtools|playwright)$/u,
  /^(?:Streamable HTTP|Server-Sent Events \(SSE\))$/u,
  /^(?:AFold3 \/ OpenFold3|Boltz2)（NVIDIA NIM）$/u,
  /^(?:https?:\/\/|[\w.-]+@)[^\s]+$/u,
  /^(?:[\w.-]+\/)+[\w.*-]*$/u,
  /^[A-Z][A-Z0-9_]*$/u,
  /^(?:[a-z0-9-]+\.)+[a-z]{2,}$/u,
  /^(?:example-custom|my-mcp-server|synonbiomed-\.\.\.)$/u,
  /^read write$/u,
];
const auditedJsxAttributes = new Set(['alt', 'aria-label', 'placeholder', 'title']);

describe('renderer i18n source audit', () => {
  it('resolves every statically declared translation key', () => {
    const missing: string[] = [];

    for (const filePath of rendererSourceFiles()) {
      const source = parseSourceFile(filePath);
      visit(source, (node) => {
        if (!ts.isCallExpression(node) || !isTranslationCall(node, source)) return;
        const key = staticText(node.arguments[0]);
        if (!key || resourcePathExists(key)) return;
        const position = source.getLineAndCharacterOfPosition(node.getStart(source));
        missing.push(`${relativePath(filePath)}:${position.line + 1} -> ${key}`);
      });
    }

    expect(missing, `Missing translation resources:\n${missing.join('\n')}`).toEqual([]);
  });

  it('does not leave Chinese user-facing literals outside locale resources', () => {
    const literals: string[] = [];

    for (const filePath of rendererSourceFiles()) {
      const source = parseSourceFile(filePath);
      visit(source, (node) => {
        if (!isUserFacingLiteral(node) || !/[\u3400-\u9fff]/u.test(node.text)) return;
        if (isAllowedChineseLiteral(filePath, node.text)) return;
        const position = source.getLineAndCharacterOfPosition(node.getStart(source));
        const normalized = JSON.stringify(node.text.replace(/\s+/gu, ' ').trim());
        const display = normalized.length > 140 ? `${normalized.slice(0, 137)}...` : normalized;
        literals.push(`${relativePath(filePath)}:${position.line + 1} -> ${display}`);
      });
    }

    expect(literals, `Chinese literals must move to locale resources:\n${literals.join('\n')}`).toEqual([]);
  });

  it('does not bypass locale resources with translation defaults', () => {
    const defaults: string[] = [];

    for (const filePath of rendererSourceFiles()) {
      const source = parseSourceFile(filePath);
      visit(source, (node) => {
        if (ts.isCallExpression(node) && isTranslationCall(node, source)) {
          const options = node.arguments[1];
          if (!options || !ts.isObjectLiteralExpression(options)) return;
          const fallback = options.properties.find(
            (property) => ts.isPropertyAssignment(property) && property.name.getText(source) === 'defaultValue'
          );
          if (!fallback) return;
          const position = source.getLineAndCharacterOfPosition(fallback.getStart(source));
          defaults.push(`${relativePath(filePath)}:${position.line + 1} -> defaultValue`);
          return;
        }

        if (
          ts.isBinaryExpression(node) &&
          [ts.SyntaxKind.BarBarToken, ts.SyntaxKind.QuestionQuestionToken].includes(node.operatorToken.kind) &&
          ts.isCallExpression(node.left) &&
          isTranslationCall(node.left, source)
        ) {
          const fallback = staticText(node.right);
          if (!fallback?.trim()) return;
          const position = source.getLineAndCharacterOfPosition(node.right.getStart(source));
          defaults.push(`${relativePath(filePath)}:${position.line + 1} -> literal fallback`);
        }
      });
    }

    expect(defaults, `Translation defaults must move to locale resources:\n${defaults.join('\n')}`).toEqual([]);
  });

  it('does not leave generic English copy in JSX', () => {
    const literals: string[] = [];

    for (const filePath of rendererSourceFiles()) {
      const source = parseSourceFile(filePath);
      visit(source, (node) => {
        const literal = jsxUiLiteral(node);
        if (!literal || !/[A-Za-z]{2}/u.test(literal) || isTechnicalUiLiteral(literal)) return;
        const position = source.getLineAndCharacterOfPosition(node.getStart(source));
        literals.push(`${relativePath(filePath)}:${position.line + 1} -> ${JSON.stringify(literal)}`);
      });
    }

    expect(literals, `English JSX copy must move to locale resources:\n${literals.join('\n')}`).toEqual([]);
  });

  it('does not bypass localization or expose raw errors in user notifications', () => {
    const violations: string[] = [];

    for (const filePath of rendererSourceFiles()) {
      const source = parseSourceFile(filePath);
      visit(source, (node) => {
        if (!ts.isCallExpression(node) || !isUserNotificationCall(node, source)) return;
        const argument = node.arguments[0];
        if (!argument) return;

        const issue = notificationArgumentIssue(argument);
        if (!issue) return;
        const position = source.getLineAndCharacterOfPosition(argument.getStart(source));
        violations.push(`${relativePath(filePath)}:${position.line + 1} -> ${issue}`);
      });
    }

    expect(
      violations,
      `User notifications must use locale resources and safe error mappings:\n${violations.join('\n')}`
    ).toEqual([]);
  });
});

function rendererSourceFiles(): string[] {
  return walk(rendererRoot).filter(
    (filePath) =>
      (filePath.endsWith('.ts') || filePath.endsWith('.tsx')) &&
      !filePath.startsWith(localeRoot) &&
      !filePath.endsWith('.d.ts')
  );
}

function walk(directory: string): string[] {
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const entryPath = path.join(directory, entry.name);
    return entry.isDirectory() ? walk(entryPath) : [entryPath];
  });
}

function parseSourceFile(filePath: string): ts.SourceFile {
  return ts.createSourceFile(
    filePath,
    fs.readFileSync(filePath, 'utf8'),
    ts.ScriptTarget.Latest,
    true,
    filePath.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  );
}

function visit(node: ts.Node, callback: (node: ts.Node) => void): void {
  callback(node);
  node.forEachChild((child) => visit(child, callback));
}

function isTranslationCall(node: ts.CallExpression, source: ts.SourceFile): boolean {
  const callee = node.expression;
  if (ts.isIdentifier(callee)) return callee.text === 't' || callee.text === 'translate';
  if (!ts.isPropertyAccessExpression(callee)) return false;
  if (callee.name.text === 't') return true;
  return callee.name.text === 'current' && /translat/i.test(callee.expression.getText(source));
}

function staticText(node: ts.Expression | undefined): string | null {
  return node && (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) ? node.text : null;
}

function resourcePathExists(key: string): boolean {
  if (resourcePaths.has(key)) return true;
  for (const resourcePath of resourcePaths) {
    if (resourcePath.startsWith(`${key}_`)) return true;
  }
  return false;
}

function collectResourcePaths(value: unknown, prefix = '', paths = new Set<string>()): Set<string> {
  if (prefix) paths.add(prefix);
  if (!value || typeof value !== 'object') return paths;
  for (const [key, child] of Object.entries(value)) {
    collectResourcePaths(child, prefix ? `${prefix}.${key}` : key, paths);
  }
  return paths;
}

function isUserFacingLiteral(node: ts.Node): node is ts.Node & { readonly text: string } {
  if (ts.isJsxText(node)) return node.text.trim().length > 0;
  if (
    !ts.isStringLiteral(node) &&
    !ts.isNoSubstitutionTemplateLiteral(node) &&
    node.kind !== ts.SyntaxKind.TemplateHead &&
    node.kind !== ts.SyntaxKind.TemplateMiddle &&
    node.kind !== ts.SyntaxKind.TemplateTail
  ) {
    return false;
  }
  return !ts.isImportDeclaration(node.parent) && !ts.isExportDeclaration(node.parent);
}

function isAllowedChineseLiteral(filePath: string, literal: string): boolean {
  const relative = relativePath(filePath);

  // These files contain language-identification data or explicit, tested zh/en formatters.
  if (bilingualOrDomainDataFiles.has(relative)) return true;
  return allowedChineseLiterals[relative]?.some((pattern) => pattern.test(literal)) ?? false;
}

function jsxUiLiteral(node: ts.Node): string | null {
  if (ts.isJsxText(node)) return normalizeLiteral(node.text);
  if (!ts.isJsxAttribute(node) || !auditedJsxAttributes.has(node.name.getText())) return null;
  return node.initializer && ts.isStringLiteral(node.initializer) ? normalizeLiteral(node.initializer.text) : null;
}

function normalizeLiteral(value: string): string {
  return value.replace(/\s+/gu, ' ').trim();
}

function isTechnicalUiLiteral(value: string): boolean {
  return technicalUiLiterals.some((pattern) => pattern.test(value));
}

function isUserNotificationCall(node: ts.CallExpression, source: ts.SourceFile): boolean {
  if (!ts.isPropertyAccessExpression(node.expression)) return false;
  if (!['error', 'warning', 'success', 'info'].includes(node.expression.name.text)) return false;
  return /message|notification/iu.test(node.expression.expression.getText(source));
}

function notificationArgumentIssue(node: ts.Expression): string | null {
  if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
    return node.text.trim() ? `literal ${JSON.stringify(node.text)}` : null;
  }
  return containsRawErrorText(node) ? 'raw exception text' : null;
}

function containsRawErrorText(node: ts.Node): boolean {
  if (
    ts.isPropertyAccessExpression(node) &&
    node.name.text === 'message' &&
    ts.isIdentifier(node.expression) &&
    /^(?:err|error|reason|exception)$/iu.test(node.expression.text)
  ) {
    return true;
  }
  if (
    ts.isCallExpression(node) &&
    ts.isIdentifier(node.expression) &&
    node.expression.text === 'String' &&
    node.arguments.some(
      (argument) => ts.isIdentifier(argument) && /^(?:err|error|reason|exception)$/iu.test(argument.text)
    )
  ) {
    return true;
  }

  let found = false;
  node.forEachChild((child) => {
    if (!found && containsRawErrorText(child)) found = true;
  });
  return found;
}

function relativePath(filePath: string): string {
  return path.relative(process.cwd(), filePath).replaceAll(path.sep, '/');
}
