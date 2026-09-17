import enPreview from './locales/en-US/preview.json';
import zhPreview from './locales/zh-CN/preview.json';

type PreviewRegionTextKey = keyof typeof enPreview.regionComment;

export function previewRegionText(
  language: string | undefined,
  key: PreviewRegionTextKey,
  values: Record<string, string | number> = {}
): string {
  const chinese = language?.toLowerCase().startsWith('zh') ?? false;
  const catalog = chinese ? zhPreview.regionComment : enPreview.regionComment;
  return catalog[key].replace(/\{\{([A-Za-z0-9_]+)\}\}/gu, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(values, name) ? String(values[name]) : match
  );
}
