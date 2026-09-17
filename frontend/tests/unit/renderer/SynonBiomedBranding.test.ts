import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';

type LoginLocale = {
  pageTitle: string;
  brand: string;
  subtitle: string;
  footerPrimary: string;
  footerSecondary: string;
};

const readLoginLocale = (locale: string): LoginLocale => {
  const filePath = path.join(
    process.cwd(),
    'packages/desktop/src/renderer/services/i18n/locales',
    locale,
    'login.json'
  );
  return JSON.parse(fs.readFileSync(filePath, 'utf8')) as LoginLocale;
};

describe('Synon Biomed global branding', () => {
  it('ships the approved circuit-tree artwork at every small product-logo size', () => {
    const brandingRoot = path.join(process.cwd(), 'public');
    const source = fs.readFileSync(path.join(brandingRoot, 'branding/synon-biomed-circuit-tree.png'));
    expect(createHash('sha256').update(source).digest('hex')).toBe(
      '9b986028e32af159366ee3ecf88b9ffcf2432adc64e45a79a18e62ff2c4a547b'
    );

    for (const size of [180, 192, 512]) {
      const png = fs.readFileSync(path.join(brandingRoot, `pwa/icon-${size}.png`));
      expect(png.subarray(1, 4).toString('ascii')).toBe('PNG');
      expect(png.readUInt32BE(16)).toBe(size);
      expect(png.readUInt32BE(20)).toBe(size);
    }
  });

  it('brands the login experience as Synon Biomed instead of SynonAI', () => {
    const zh = readLoginLocale('zh-CN');
    const en = readLoginLocale('en-US');

    expect(zh.pageTitle).toBe('Synon Biomed - \u767b\u5f55');
    expect(zh.brand).toBe('Synon Biomed');
    expect(zh.footerPrimary).toBe('\u751f\u7269\u533b\u836f AI \u5de5\u4f5c\u53f0');
    expect(zh.footerSecondary).toBe('\u9879\u76ee\u3001\u4e13\u5bb6\u3001Skill \u4e0e MCP \u4e00\u4f53\u5316');
    expect(en.pageTitle).toBe('Synon Biomed - Sign In');
    expect(en.brand).toBe('Synon Biomed');
    expect(en.footerPrimary).toBe('Biomedical AI workbench');
    expect(en.footerSecondary).toBe('Projects, experts, skills, and MCP in one place');
    expect(JSON.stringify([zh, en])).not.toContain('SynonAI');
  });

  it('does not hard-code the old SynonAI titlebar brand', () => {
    const titlebarSource = fs.readFileSync(
      path.join(process.cwd(), 'packages/desktop/src/renderer/components/layout/Titlebar/index.tsx'),
      'utf8'
    );

    expect(titlebarSource).toContain("const appTitle = useMemo(() => 'Synon Biomed', [])");
    expect(titlebarSource).not.toContain("'SynonAI'");
  });
  it('removes the legacy SynonAI product feedback affordance from the titlebar', () => {
    const titlebarSource = fs.readFileSync(
      path.join(process.cwd(), 'packages/desktop/src/renderer/components/layout/Titlebar/index.tsx'),
      'utf8'
    );

    expect(titlebarSource).not.toContain('useFeedback');
    expect(titlebarSource).not.toContain('openFeedback');
    expect(titlebarSource).not.toContain('FeedbackIcon');
    expect(titlebarSource).not.toContain('quickActionFeedback');
    expect(titlebarSource).not.toContain('Report Issue');
  });
});
