import { ipcBridge } from '@/common';
import { joinPath } from '@/common/chat/chatLib';
import { LoadingTwo } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { createContext } from '@renderer/utils/ui/createContext';
import { iconColors } from '@/renderer/styles/colors';
import { useNearViewport } from '@/renderer/hooks/ui/useNearViewport';

const [useLocalImage, LocalImageProvider, useUpdateLocalImage] = createContext({ root: '' });

const LocalImageView: React.FC<{
  src: string;
  alt: string;
  className?: string;
}> & {
  Provider: typeof LocalImageProvider;
  useUpdateLocalImage: typeof useUpdateLocalImage;
} = ({ src, alt, className }) => {
  const [loading, setLoading] = useState(true);
  const [url, setUrl] = useState(src);
  const { root } = useLocalImage();
  const { ref, isNearViewport } = useNearViewport<HTMLSpanElement>();

  const absolutePath = useMemo(() => {
    if (!root) return src;
    if (
      src.startsWith('http') ||
      src.startsWith('data:') ||
      src.startsWith('/') ||
      src.startsWith('file:') ||
      src.startsWith('\\') ||
      /^[A-Za-z]:/.test(src)
    ) {
      return src;
    }
    return joinPath(root, src);
  }, [src, root]);

  useEffect(() => {
    if (!isNearViewport) return;
    let active = true;
    setLoading(true);
    setUrl(src);
    ipcBridge.fs.getImageBase64
      .invoke({ path: absolutePath, workspace: root || undefined })
      .then((base64) => {
        if (!active) return;
        if (base64) {
          setUrl(base64);
        }
        setLoading(false);
      })
      .catch((error) => {
        if (!active) return;
        console.error('[LocalImageView] Failed to load image:', {
          path: absolutePath,
          error,
        });
        setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [absolutePath, isNearViewport, src]);
  if (loading)
    return (
      <span ref={ref} style={{ display: 'flex', alignItems: 'center', gap: '4px' }}>
        <LoadingTwo
          className='loading'
          style={{ display: 'flex' }}
          theme='outline'
          size='14'
          fill={iconColors.primary}
          strokeWidth={2}
        />
        <span>{alt}</span>
      </span>
    );
  return <img src={url} alt={alt} className={className} loading='lazy' decoding='async' />;
};

LocalImageView.Provider = LocalImageProvider;
LocalImageView.useUpdateLocalImage = useUpdateLocalImage;

export default LocalImageView;
