import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Button, Input } from '@arco-design/web-react';
import { Left, Right, Refresh } from '@icon-park/react';
import { useTranslation } from 'react-i18next';
import PreviewLoadingState from './PreviewLoadingState';

const PREVIEW_LOAD_TIMEOUT_MS = 20_000;

export interface WebviewHostProps {
  url: string;
  id?: string;
  showNavBar?: boolean;
  partition?: string;
  title?: string;
  className?: string;
  style?: React.CSSProperties;
  onDidFinishLoad?: () => void;
  onDidFailLoad?: (errorCode: number, errorDescription: string) => void;
}

const WebviewHost: React.FC<WebviewHostProps> = ({
  url,
  id,
  showNavBar = false,
  title,
  className,
  style,
  onDidFinishLoad,
  onDidFailLoad,
}) => {
  const { t } = useTranslation();
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const backStack = useRef<string[]>([]);
  const forwardStack = useRef<string[]>([]);
  const [currentUrl, setCurrentUrl] = useState(url);
  const [inputUrl, setInputUrl] = useState(url);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<'timeout' | 'failed' | null>(null);
  const [historyRevision, setHistoryRevision] = useState(0);

  useEffect(() => {
    setCurrentUrl(url);
    setInputUrl(url);
    setLoading(true);
    setLoadError(null);
    backStack.current = [];
    forwardStack.current = [];
    setHistoryRevision((value) => value + 1);
  }, [url]);

  const navigate = useCallback(
    (nextUrl: string, record = true) => {
      const normalized = nextUrl.trim();
      if (!normalized || normalized === currentUrl) return;
      if (record) {
        backStack.current.push(currentUrl);
        forwardStack.current = [];
      }
      setCurrentUrl(normalized);
      setInputUrl(normalized);
      setLoading(true);
      setLoadError(null);
      setHistoryRevision((value) => value + 1);
    },
    [currentUrl]
  );

  const goBack = () => {
    const previous = backStack.current.pop();
    if (!previous) return;
    forwardStack.current.push(currentUrl);
    navigate(previous, false);
  };

  const goForward = () => {
    const next = forwardStack.current.pop();
    if (!next) return;
    backStack.current.push(currentUrl);
    navigate(next, false);
  };

  const refresh = () => {
    setLoading(true);
    setLoadError(null);
    const frame = frameRef.current;
    if (frame) frame.src = currentUrl;
  };

  useEffect(() => {
    if (!loading) return;
    const timeout = window.setTimeout(() => {
      setLoading(false);
      setLoadError('timeout');
      onDidFailLoad?.(-2, t('preview.web.loadTimeout'));
    }, PREVIEW_LOAD_TIMEOUT_MS);
    return () => window.clearTimeout(timeout);
  }, [currentUrl, historyRevision, loading, onDidFailLoad, t]);

  const frameTitle = title || t('preview.web.frameTitle');

  return (
    <div className={`size-full min-h-0 flex flex-col ${className ?? ''}`} style={style} data-preview-id={id}>
      {showNavBar && (
        <div className='h-40px shrink-0 flex items-center gap-4px px-8px border-b border-b-solid border-arco-1'>
          <Button
            type='text'
            icon={<Left />}
            disabled={backStack.current.length === 0}
            onClick={goBack}
            aria-label={t('preview.web.back')}
            title={t('preview.web.back')}
          />
          <Button
            type='text'
            icon={<Right />}
            disabled={forwardStack.current.length === 0}
            onClick={goForward}
            aria-label={t('preview.web.forward')}
            title={t('preview.web.forward')}
          />
          <Button
            type='text'
            icon={<Refresh />}
            onClick={refresh}
            aria-label={t('preview.web.refresh')}
            title={t('preview.web.refresh')}
          />
          <Input
            value={inputUrl}
            onChange={setInputUrl}
            onPressEnter={() => navigate(inputUrl)}
            aria-label={t('preview.web.urlLabel')}
          />
        </div>
      )}
      <div className='relative flex-1 min-h-0'>
        {loading && (
          <div className='absolute inset-0 z-1 flex items-center justify-center bg-1'>
            <PreviewLoadingState />
          </div>
        )}
        {loadError && !loading && (
          <div className='absolute inset-0 z-1 flex flex-col items-center justify-center gap-12px bg-1 px-24px text-center'>
            <p className='m-0 text-12px leading-20px text-t-secondary'>
              {t(loadError === 'timeout' ? 'preview.web.loadTimeout' : 'preview.web.loadFailed')}
            </p>
            <div className='flex items-center gap-8px'>
              <Button icon={<Refresh theme='outline' size={15} />} onClick={refresh}>
                {t('common.retry')}
              </Button>
              <a href={currentUrl} target='_blank' rel='noreferrer' className='text-12px text-t-primary'>
                {t('preview.web.openOriginal')}
              </a>
            </div>
          </div>
        )}
        <iframe
          ref={frameRef}
          key={`${currentUrl}:${historyRevision}`}
          src={currentUrl}
          title={frameTitle}
          className='size-full border-0 bg-1'
          sandbox='allow-downloads allow-forms allow-modals allow-popups allow-same-origin allow-scripts'
          onLoad={() => {
            setLoading(false);
            setLoadError(null);
            onDidFinishLoad?.();
          }}
          onError={() => {
            setLoading(false);
            setLoadError('failed');
            onDidFailLoad?.(-1, t('preview.web.loadFailed'));
          }}
        />
      </div>
    </div>
  );
};

export default WebviewHost;
