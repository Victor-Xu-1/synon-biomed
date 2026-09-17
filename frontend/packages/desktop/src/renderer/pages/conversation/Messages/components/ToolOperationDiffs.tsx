import React, { useMemo } from 'react';
import { createTwoFilesPatch } from 'diff';
import { useTranslation } from 'react-i18next';
import FileChangesPanel from '@/renderer/components/base/FileChangesPanel';
import { useDiffPreviewHandlers } from '@/renderer/hooks/file/useDiffPreviewHandlers';
import { parseDiff } from '@/renderer/utils/file/diffUtils';
import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';

const ToolOperationDiff: React.FC<{
  diff: NonNullable<NormalizedToolCall['fileDiffs']>[number];
}> = ({ diff }) => {
  const { t } = useTranslation();
  const displayName = diff.path.split(/[/\\]/u).pop() || diff.path || t('tools.unknownFile');
  const formattedDiff = useMemo(
    () =>
      createTwoFilesPatch(displayName, displayName, diff.oldText, diff.newText, '', '', {
        context: 3,
      }),
    [diff.newText, diff.oldText, displayName]
  );
  const fileInfo = useMemo(() => parseDiff(formattedDiff, displayName), [displayName, formattedDiff]);
  const { handleFileClick, handleDiffClick } = useDiffPreviewHandlers({
    diffText: formattedDiff,
    display_name: displayName,
    file_path: diff.path || displayName,
  });
  return (
    <FileChangesPanel
      title={displayName}
      files={[fileInfo]}
      onFileClick={handleFileClick}
      onDiffClick={handleDiffClick}
      defaultExpanded
    />
  );
};

const ToolOperationDiffs: React.FC<{
  diffs: NonNullable<NormalizedToolCall['fileDiffs']>;
}> = ({ diffs }) => (
  <div className='tool-operation-diffs'>
    {diffs.map((diff, index) => (
      <ToolOperationDiff key={`${diff.path}:${index}`} diff={diff} />
    ))}
  </div>
);

export default React.memo(ToolOperationDiffs);
