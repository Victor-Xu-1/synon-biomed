/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import {
  loadLightweightOfficePreview,
  type LightweightOfficeKind,
  type LightweightOfficePresentation,
  type LightweightOfficePreviewData,
  type LightweightOfficeWorkbook,
} from '@/renderer/services/lightweightOfficePreview';
import { Button, Empty } from '@arco-design/web-react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import MarkdownViewer from './MarkdownViewer';

export type LightweightOfficeViewerProps = {
  docType: LightweightOfficeKind;
  file_path?: string;
  artifactId?: string;
  versionId?: string;
  content?: string;
  workspace?: string;
};

const loadingLabelKeys = {
  word: 'preview.lightweightOffice.loadingWord',
  excel: 'preview.lightweightOffice.loadingWorkbook',
  ppt: 'preview.lightweightOffice.loadingPresentation',
};

const LightweightOfficeViewer: React.FC<LightweightOfficeViewerProps> = ({
  docType,
  file_path,
  artifactId,
  versionId,
  workspace,
}) => {
  const { t } = useTranslation();
  const [data, setData] = useState<LightweightOfficePreviewData | null>(null);
  const [error, setError] = useState(false);
  const [retryKey, setRetryKey] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setData(null);
    setError(false);
    void loadLightweightOfficePreview(
      docType,
      { filePath: file_path, artifactId, versionId, workspace },
      { signal: controller.signal }
    )
      .then((result) => {
        if (!controller.signal.aborted) setData(result);
      })
      .catch(() => {
        if (!controller.signal.aborted) setError(true);
      });
    return () => controller.abort();
  }, [artifactId, docType, file_path, retryKey, versionId, workspace]);

  if (error) {
    return (
      <div className='size-full min-h-240px flex-center bg-1 px-24px' role='alert'>
        <Empty
          description={
            <div className='flex flex-col items-center gap-10px'>
              <span className='text-13px text-t-secondary'>{t('preview.lightweightOffice.unavailable')}</span>
              <Button size='small' onClick={() => setRetryKey((value) => value + 1)}>
                {t('common.retry')}
              </Button>
            </div>
          }
        />
      </div>
    );
  }

  if (data === null) return <PreviewLoadingState label={t(loadingLabelKeys[docType])} />;

  if (docType === 'word') {
    return (
      <div className='size-full min-h-0 overflow-auto bg-1' data-testid='lightweight-office-word'>
        <MarkdownViewer content={typeof data === 'string' ? data : ''} />
      </div>
    );
  }

  if (docType === 'excel') {
    return <WorkbookPreview workbook={data as LightweightOfficeWorkbook} />;
  }

  return <PresentationPreview presentation={data as LightweightOfficePresentation} />;
};

const WorkbookPreview: React.FC<{ workbook: LightweightOfficeWorkbook }> = ({ workbook }) => {
  const { t } = useTranslation();
  const sheets = Array.isArray(workbook.sheets) ? workbook.sheets : [];
  const [activeIndex, setActiveIndex] = useState(0);
  useEffect(() => setActiveIndex(0), [workbook]);
  const activeSheet = sheets[Math.min(activeIndex, Math.max(0, sheets.length - 1))];
  const columnCount = useMemo(
    () => activeSheet?.data.reduce((maximum, row) => Math.max(maximum, row.length), 0) ?? 0,
    [activeSheet]
  );

  if (!activeSheet) {
    return <Empty className='size-full flex-center' description={t('preview.lightweightOffice.noWorksheets')} />;
  }

  return (
    <div className='size-full min-h-0 flex flex-col bg-1' data-testid='lightweight-office-excel'>
      <div
        className='shrink-0 flex items-center gap-4px overflow-x-auto border-0 border-b border-solid border-[var(--color-border-2)] px-10px py-8px'
        role='tablist'
        aria-label={t('preview.lightweightOffice.worksheets')}
      >
        {sheets.map((sheet, index) => (
          <button
            key={`${sheet.name}-${index}`}
            type='button'
            role='tab'
            aria-selected={index === activeIndex}
            className={`shrink-0 border-0 px-10px py-6px text-12px cursor-pointer ${
              index === activeIndex ? 'bg-fill-2 text-t-primary' : 'bg-transparent text-t-secondary hover:bg-fill-1'
            }`}
            onClick={() => setActiveIndex(index)}
          >
            {sheet.name || t('preview.lightweightOffice.worksheetNumber', { number: index + 1 })}
          </button>
        ))}
      </div>
      <div className='min-h-0 flex-1 overflow-auto bg-1 p-12px'>
        <table className='min-w-full border-collapse text-12px leading-18px text-t-primary'>
          <tbody>
            {activeSheet.data.map((row, rowIndex) => (
              <tr key={rowIndex}>
                <th className='sticky left-0 min-w-42px border border-solid border-[var(--color-border-2)] bg-fill-1 px-8px py-6px text-center font-normal text-t-tertiary'>
                  {rowIndex + 1}
                </th>
                {Array.from({ length: columnCount }, (_, columnIndex) => (
                  <td
                    key={columnIndex}
                    className='min-w-110px max-w-360px border border-solid border-[var(--color-border-2)] bg-1 px-9px py-6px align-top whitespace-pre-wrap break-words'
                  >
                    {formatCell(row[columnIndex])}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
};

const PresentationPreview: React.FC<{ presentation: LightweightOfficePresentation }> = ({ presentation }) => {
  const { t } = useTranslation();
  const slides = Array.isArray(presentation.slides) ? presentation.slides : [];
  if (slides.length === 0) {
    return <Empty className='size-full flex-center' description={t('preview.lightweightOffice.noSlides')} />;
  }
  return (
    <div
      className='size-full min-h-0 overflow-auto bg-fill-1 px-24px py-20px'
      data-testid='lightweight-office-ppt'
      aria-label={t('preview.lightweightOffice.slidePreview')}
    >
      <div className='mx-auto flex max-w-960px flex-col gap-20px'>
        {slides.map((slide, slideIndex) => {
          const paragraphs = Array.isArray(slide.content?.paragraphs)
            ? slide.content.paragraphs
            : String(slide.content?.text ?? '')
                .split('\n')
                .filter(Boolean);
          return (
            <section
              key={`${slide.slideNumber}-${slideIndex}`}
              role='region'
              aria-label={t('preview.lightweightOffice.slideNumber', { number: slide.slideNumber })}
            >
              <div className='mb-6px text-11px tabular-nums text-t-tertiary'>
                {t('preview.lightweightOffice.slideNumber', { number: slide.slideNumber })}
              </div>
              <div
                className='relative flex min-h-360px flex-col justify-center overflow-hidden bg-white text-black shadow-sm'
                style={{ aspectRatio: '16 / 9', padding: '8% 10%' }}
              >
                {paragraphs.map((paragraph, index) => (
                  <p
                    key={`${index}-${paragraph.slice(0, 24)}`}
                    className={`m-0 text-pretty ${index === 0 ? 'mb-18px text-28px font-600 leading-36px' : 'mb-10px text-17px leading-27px'}`}
                  >
                    {paragraph}
                  </p>
                ))}
              </div>
            </section>
          );
        })}
      </div>
    </div>
  );
};

function formatCell(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

export default LightweightOfficeViewer;
