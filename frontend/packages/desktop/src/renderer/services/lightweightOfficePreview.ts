import { requestSynonBiomedJson, type SynonBiomedGatewayOptions } from './synonBiomedHttp';

export type LightweightOfficeKind = 'word' | 'excel' | 'ppt';

export type LightweightOfficeSource = {
  filePath?: string;
  artifactId?: string;
  versionId?: string;
  workspace?: string;
};

export type LightweightOfficeWorkbook = {
  sheets: Array<{
    name: string;
    data: unknown[][];
    merges?: Array<{ s: { r: number; c: number }; e: { r: number; c: number } }>;
  }>;
};

export type LightweightOfficePresentation = {
  slides: Array<{
    slideNumber: number;
    content: { text?: string; paragraphs?: string[] };
  }>;
};

export type LightweightOfficePreviewData = string | LightweightOfficeWorkbook | LightweightOfficePresentation;

type DocumentConversionEnvelope = {
  to: string;
  result?: {
    success: boolean;
    data?: unknown;
    error?: string;
  };
};

const targetFormat: Record<LightweightOfficeKind, string> = {
  word: 'markdown',
  excel: 'excel-json',
  ppt: 'ppt-json',
};

export async function loadLightweightOfficePreview(
  kind: LightweightOfficeKind,
  source: LightweightOfficeSource,
  options: SynonBiomedGatewayOptions = {}
): Promise<LightweightOfficePreviewData> {
  const artifactId = source.artifactId?.trim();
  const filePath = artifactId ? undefined : source.filePath?.trim();
  const versionId = source.versionId?.trim();
  if (!filePath && !artifactId) {
    throw new Error('Office preview requires a document source');
  }
  if (versionId && !artifactId) {
    throw new Error('Office preview version requires an artifact source');
  }

  const payload = await requestSynonBiomedJson<DocumentConversionEnvelope>(
    '/api/document/convert',
    {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        ...(filePath ? { file_path: filePath } : {}),
        ...(artifactId ? { artifact_id: artifactId } : {}),
        ...(versionId ? { version_id: versionId } : {}),
        ...(filePath && source.workspace ? { workspace: source.workspace } : {}),
        to: targetFormat[kind],
      }),
    },
    { ...options, timeoutMs: options.timeoutMs ?? 30_000 }
  );
  if (!payload.result?.success || payload.result.data === undefined) {
    throw new Error(payload.result?.error || 'The document could not be converted for preview');
  }
  return validatePreviewData(kind, payload.result.data);
}

function validatePreviewData(kind: LightweightOfficeKind, value: unknown): LightweightOfficePreviewData {
  if (kind === 'word') {
    if (typeof value !== 'string') throw new Error('Invalid document preview response');
    return value;
  }
  if (kind === 'excel') return validateWorkbook(value);
  return validatePresentation(value);
}

function validateWorkbook(value: unknown): LightweightOfficeWorkbook {
  if (!isRecord(value) || !Array.isArray(value.sheets) || value.sheets.length > 1_000) {
    throw new Error('Invalid workbook preview response');
  }
  let cellCount = 0;
  const sheets = value.sheets.map((sheet, sheetIndex) => {
    if (!isRecord(sheet) || !Array.isArray(sheet.data) || sheet.data.length > 10_000) {
      throw new Error('Invalid workbook preview response');
    }
    const data = sheet.data.map((row) => {
      if (!Array.isArray(row) || row.length > 1_000) throw new Error('Invalid workbook preview response');
      cellCount += row.length;
      if (cellCount > 100_000) throw new Error('Workbook preview is too large');
      return row;
    });
    return {
      name: typeof sheet.name === 'string' && sheet.name.trim() ? sheet.name : `Sheet ${sheetIndex + 1}`,
      data,
    };
  });
  return { sheets };
}

function validatePresentation(value: unknown): LightweightOfficePresentation {
  if (!isRecord(value) || !Array.isArray(value.slides) || value.slides.length > 10_000) {
    throw new Error('Invalid presentation preview response');
  }
  let characterCount = 0;
  const slides = value.slides.map((slide, index) => {
    if (!isRecord(slide) || !isRecord(slide.content)) {
      throw new Error('Invalid presentation preview response');
    }
    const text = typeof slide.content.text === 'string' ? slide.content.text : '';
    const paragraphs = Array.isArray(slide.content.paragraphs)
      ? slide.content.paragraphs.filter((paragraph): paragraph is string => typeof paragraph === 'string')
      : text.split('\n').filter(Boolean);
    characterCount += text.length + paragraphs.reduce((total, paragraph) => total + paragraph.length, 0);
    if (characterCount > 4_000_000) throw new Error('Presentation preview is too large');
    return {
      slideNumber:
        typeof slide.slideNumber === 'number' && Number.isInteger(slide.slideNumber) && slide.slideNumber > 0
          ? slide.slideNumber
          : index + 1,
      content: { text, paragraphs },
    };
  });
  return { slides };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
