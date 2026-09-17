import type { SynonBiomedExecutionRecord } from './runtimeOperationsModel';
import { Button, Empty, Spin } from '@arco-design/web-react';
import React from 'react';
import { useTranslation } from 'react-i18next';
import './SynonBiomedExecutionLogContent.css';

const SynonBiomedExecutionLogContent: React.FC<{
  records: SynonBiomedExecutionRecord[];
  loading: boolean;
  error: string | null;
  pageFromLatest: number;
  total: number;
  hasEarlier: boolean;
  hasLater: boolean;
  onEarlier: () => void;
  onLater: () => void;
}> = ({ records, loading, error, pageFromLatest, total, hasEarlier, hasLater, onEarlier, onLater }) => {
  const { t, i18n } = useTranslation();
  if (loading)
    return (
      <div className='h-240px flex-center'>
        <Spin tip={t('conversation.synonRuntime.runtimeOperations.loadingExecutionLog')} />
      </div>
    );
  if (error) return <div className='px-12px py-20px text-12px text-danger-6'>{error}</div>;
  if (records.length === 0)
    return <Empty description={t('conversation.synonRuntime.runtimeOperations.noExecutionLog')} />;
  const end = Math.max(0, total - pageFromLatest * 200);
  const start = records.length === 0 ? 0 : Math.max(1, end - records.length + 1);
  return (
    <div className='synon-biomed-execution-log'>
      <nav
        className='synon-biomed-execution-log__navigation'
        aria-label={t('conversation.synonRuntime.runtimeOperations.executionLogNavigation')}
      >
        <Button
          size='mini'
          className='synon-biomed-execution-log__page-button'
          disabled={!hasEarlier || loading}
          aria-label={t('conversation.synonRuntime.runtimeOperations.executionLogEarlier')}
          onClick={onEarlier}
        >
          {t('conversation.synonRuntime.runtimeOperations.earlier')}
        </Button>
        <span data-testid='execution-log-range' className='synon-biomed-execution-log__range'>
          {t('conversation.synonRuntime.runtimeOperations.executionLogRange', {
            start,
            end,
            total,
          })}
        </span>
        <Button
          size='mini'
          className='synon-biomed-execution-log__page-button'
          disabled={!hasLater || loading}
          aria-label={t('conversation.synonRuntime.runtimeOperations.executionLogLater')}
          onClick={onLater}
        >
          {t('conversation.synonRuntime.runtimeOperations.later')}
        </Button>
      </nav>
      <div className='synon-biomed-execution-log__records'>
        {records.map((record) =>
          record.kind === 'cell' ? (
            <article
              key={record.id}
              data-testid='execution-log-record'
              role='region'
              aria-label={t('conversation.history.sessionNotebook.cellNumber', {
                number: record.cellIndex + 1,
              })}
              className='synon-biomed-execution-log__record'
            >
              <div className='synon-biomed-execution-log__record-meta'>
                <span className='synon-biomed-execution-log__record-index'>[{record.cellIndex}]</span>
                <span className='synon-biomed-execution-log__record-language'>{record.language}</span>
                <span
                  className={`synon-biomed-execution-log__record-status${
                    record.exitStatus === 'ok' ? '' : ' synon-biomed-execution-log__record-status--error'
                  }`}
                >
                  {record.exitStatus}
                </span>
                <span className='synon-biomed-execution-log__record-environment'>
                  {record.kernelKind === 'operon' ? 'REPL' : record.environment}
                </span>
              </div>
              <pre className='synon-biomed-execution-log__source'>{record.source}</pre>
              {record.stderr || record.stdout ? (
                <pre
                  className={`synon-biomed-execution-log__output${
                    record.stderr ? ' synon-biomed-execution-log__output--error' : ''
                  }`}
                >
                  {record.stderr || record.stdout}
                </pre>
              ) : null}
              {record.filesWritten.length > 0 ? (
                <div className='synon-biomed-execution-log__files'>
                  {t('conversation.synonRuntime.runtimeOperations.filesWritten', {
                    files: record.filesWritten.map(fileBasename).join(i18n.language === 'zh-CN' ? '、' : ', '),
                  })}
                </div>
              ) : null}
            </article>
          ) : (
            <div key={record.id} data-testid='execution-log-record' className='synon-biomed-execution-log__event'>
              <span className='synon-biomed-execution-log__event-label'>
                {record.kind === 'save'
                  ? record.actor
                    ? t('conversation.synonRuntime.runtimeOperations.executionSavedBy', { actor: record.actor })
                    : t('conversation.synonRuntime.runtimeOperations.executionSaved')
                  : record.kind === 'app_tool'
                    ? (record.label ?? t('conversation.synonRuntime.runtimeOperations.executionAppTool'))
                    : t('conversation.synonRuntime.runtimeOperations.executionUserEdit')}
              </span>
              {record.detail ? <span className='synon-biomed-execution-log__event-detail'>{record.detail}</span> : null}
            </div>
          )
        )}
      </div>
    </div>
  );
};

function fileBasename(path: string): string {
  return path.split(/[\\/]/).findLast(Boolean) ?? path;
}

export default SynonBiomedExecutionLogContent;
