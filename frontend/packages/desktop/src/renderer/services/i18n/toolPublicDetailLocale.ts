import enMessages from './locales/en-US/messages.json';
import zhMessages from './locales/zh-CN/messages.json';

export type ToolPublicDetailTextKey = keyof typeof enMessages.toolPublicDetail;
type InterpolationValues = Record<string, string | number>;

const englishCatalog = enMessages.toolPublicDetail;
const chineseCatalog: Record<ToolPublicDetailTextKey, string> = zhMessages.toolPublicDetail;

export function toolPublicDetailText(
  chinese: boolean,
  key: ToolPublicDetailTextKey,
  values: InterpolationValues = {}
): string {
  const template = (chinese ? chineseCatalog : englishCatalog)[key];
  return template.replace(/\{\{([A-Za-z0-9_]+)\}\}/gu, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(values, name) ? String(values[name]) : match
  );
}
