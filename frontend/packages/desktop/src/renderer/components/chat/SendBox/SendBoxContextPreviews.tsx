import type { ReplyQuote } from '@/renderer/utils/emitter';
import type { FileSelectionItem } from '@/renderer/utils/file/fileSelection';
import { Tag } from '@arco-design/web-react';
import { CloseSmall, Quote } from '@icon-park/react';
import React from 'react';
import { getSelectedItemDisplayLabel } from './selectionModel';

type DomSnippet = { id: string; tag: string };

type Props = {
  domSnippets: DomSnippet[];
  onClearReply: () => void;
  onRemoveDomSnippet: (id: string) => void;
  onRemoveExternalSelection: (item: FileSelectionItem) => void;
  replyQuote: ReplyQuote | null;
  unmatchedSelectedWorkspaceItems: FileSelectionItem[];
};

const SendBoxContextPreviews = ({
  domSnippets,
  onClearReply,
  onRemoveDomSnippet,
  onRemoveExternalSelection,
  replyQuote,
  unmatchedSelectedWorkspaceItems,
}: Props) => (
  <>
    {replyQuote && (
      <div className='flex items-start gap-10px mb-8px px-12px py-10px rd-10px bg-fill-1 b-1 b-solid b-border-2'>
        <div className='flex-shrink-0 mt-2px' style={{ lineHeight: 0 }}>
          <Quote theme='filled' size='16' fill='rgb(var(--primary-6))' />
        </div>
        <div className='flex-1 min-w-0 text-13px text-t-primary line-clamp-3 lh-20px whitespace-pre-wrap break-all'>
          {replyQuote.content}
        </div>
        <div
          className='flex-shrink-0 mt-2px p-2px rd-full cursor-pointer hover:bg-fill-3 transition-colors'
          onClick={onClearReply}
          style={{ lineHeight: 0 }}
        >
          <CloseSmall theme='outline' size='14' />
        </div>
      </div>
    )}
    {domSnippets.length > 0 && (
      <div className='flex flex-wrap gap-6px mb-8px'>
        {domSnippets.map((snippet) => (
          <Tag
            key={snippet.id}
            closable
            closeIcon={<CloseSmall theme='outline' size='12' />}
            onClose={() => onRemoveDomSnippet(snippet.id)}
            className='text-12px bg-fill-2 b-1 b-solid b-border-2 rd-4px'
          >
            {snippet.tag}
          </Tag>
        ))}
      </div>
    )}
    {unmatchedSelectedWorkspaceItems.length > 0 && (
      <div className='flex flex-wrap gap-6px mb-8px'>
        {unmatchedSelectedWorkspaceItems.map((item) => (
          <Tag
            key={typeof item === 'string' ? item : item.path}
            closable
            closeIcon={<CloseSmall theme='outline' size='12' />}
            onClose={() => onRemoveExternalSelection(item)}
            className='text-12px bg-fill-2 b-1 b-solid b-border-2 rd-4px'
          >
            {getSelectedItemDisplayLabel(item)}
          </Tag>
        ))}
      </div>
    )}
  </>
);

export default SendBoxContextPreviews;
