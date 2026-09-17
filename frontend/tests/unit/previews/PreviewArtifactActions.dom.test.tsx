import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  t: (key: string) => key,
  navigate: vi.fn(),
  loadArtifact: vi.fn(),
  loadFolders: vi.fn(),
  setPriority: vi.fn(),
  rename: vi.fn(),
  remove: vi.fn(),
  copyArtifact: vi.fn(),
  moveArtifact: vi.fn(),
  loadStorage: vi.fn(),
  loadBuckets: vi.fn(),
  exportCloud: vi.fn(),
  copyText: vi.fn(),
  download: vi.fn(),
  addToChat: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock('react-router', () => ({ useNavigate: () => mocks.navigate }));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: mocks.t }) }));
vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedArtifact: mocks.loadArtifact,
  loadSynonBiomedProjectFolders: mocks.loadFolders,
}));
vi.mock('@/renderer/services/synonBiomedArtifacts', () => ({
  setSynonBiomedArtifactPriority: mocks.setPriority,
  renameSynonBiomedArtifact: mocks.rename,
  deleteSynonBiomedArtifact: mocks.remove,
  copySynonBiomedArtifact: mocks.copyArtifact,
  moveSynonBiomedArtifact: mocks.moveArtifact,
}));
vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedStorageSettings: mocks.loadStorage,
  loadSynonBiomedCloudBuckets: mocks.loadBuckets,
  exportSynonBiomedArtifactToCloud: mocks.exportCloud,
}));
vi.mock('@/renderer/utils/ui/clipboard', () => ({ copyText: mocks.copyText }));
vi.mock('@/renderer/utils/file/download', () => ({ downloadFileFromUrl: mocks.download }));
vi.mock('@/renderer/components/chat/SendBox/composerReferenceBridge', () => ({
  insertArtifactReferenceIntoActiveComposer: mocks.addToChat,
}));
vi.mock('@/renderer/components/common/AccessibleActionDialog', () => ({
  default: ({ visible, title, children, onConfirm }: any) =>
    visible ? (
      <div role='alertdialog' aria-label={title}>
        {children}
        <button type='button' data-testid='delete-confirm' onClick={() => void onConfirm()}>
          confirm
        </button>
      </div>
    ) : null,
}));
vi.mock('@arco-design/web-react', () => {
  const Menu = ({ children, ...props }: any) => <div {...props}>{children}</div>;
  Menu.Item = ({ children, onClick, disabled }: any) => (
    <button type='button' disabled={disabled} onClick={onClick}>
      {children}
    </button>
  );
  const Modal = ({ visible, title, children, onOk }: any) =>
    visible ? (
      <div role='dialog' aria-label={title}>
        {children}
        <button type='button' data-testid='modal-ok' onClick={onOk}>
          ok
        </button>
      </div>
    ) : null;
  const Input = ({ value, onChange, ...props }: any) => (
    <input {...props} value={value} onChange={(event) => onChange(event.target.value)} />
  );
  const Select = ({ value, onChange, children, ...props }: any) => (
    <select {...props} value={value} onChange={(event) => onChange(event.target.value)}>
      {children}
    </select>
  );
  Select.Option = ({ value, children }: any) => <option value={value}>{children}</option>;
  return {
    Dropdown: ({ children, droplist, popupVisible, onVisibleChange }: any) => (
      <>
        {React.cloneElement(children, {
          onClick: () => onVisibleChange?.(!popupVisible),
        })}
        {popupVisible ? droplist : null}
      </>
    ),
    Empty: ({ description }: any) => <div>{description}</div>,
    Input,
    Menu,
    Modal,
    Select,
    Message: { useMessage: () => [{ success: mocks.success, error: mocks.error }, null] },
  };
});

import PreviewArtifactActions from '@/renderer/pages/conversation/Preview/components/PreviewPanel/PreviewArtifactActions';

const clickAndFlush = async (element: HTMLElement) => {
  await act(async () => {
    fireEvent.click(element);
    await Promise.resolve();
  });
};

const artifact = {
  artifactId: 'artifact-1',
  versionId: 'version-1',
  versionNumber: 1,
  projectId: 'project-1',
  rootFrameId: 'frame-1',
  frameId: 'frame-1',
  creatingFrameId: 'frame-1',
  filename: 'report.md',
  contentType: 'text/markdown',
  sizeBytes: 10,
  createdAt: null,
  updatedAt: null,
  checksum: null,
  filePath: null,
  folderId: null,
  priority: null,
  isUserUpload: false,
  agentName: null,
  isIntermediate: false,
};

describe('PreviewArtifactActions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.location.hash = '#/conversation/frame-1';
    mocks.loadArtifact.mockResolvedValue(artifact);
    mocks.loadFolders.mockResolvedValue([{ folderId: 'folder-1', name: 'Results' }]);
    mocks.loadStorage.mockResolvedValue({ cloudCredentials: [{ id: 'cloud-1', name: 'Cloud', connected: true }] });
    mocks.loadBuckets.mockResolvedValue(['bucket-1']);
    mocks.setPriority.mockResolvedValue({});
    mocks.rename.mockResolvedValue({});
    mocks.remove.mockResolvedValue({});
    mocks.copyArtifact.mockResolvedValue({});
    mocks.moveArtifact.mockResolvedValue({});
    mocks.exportCloud.mockResolvedValue({});
    mocks.copyText.mockResolvedValue(undefined);
    mocks.download.mockResolvedValue(undefined);
    mocks.addToChat.mockReturnValue(true);
  });

  it('exposes every consolidated file action and wires non-destructive actions', async () => {
    render(
      <PreviewArtifactActions
        artifactId='artifact-1'
        versionId='version-1'
        fileName='report.md'
        onRenamed={vi.fn()}
        onDeleted={vi.fn()}
      />
    );
    await waitFor(() => expect(mocks.loadArtifact).toHaveBeenCalledWith('artifact-1'));
    expect(mocks.loadArtifact).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));

    const actionMenu = screen.getByRole('menu', { name: 'preview.artifact.actions.more' });
    const actionItems = within(actionMenu).getAllByRole('menuitem');
    expect(actionItems).toHaveLength(14);
    expect(actionMenu).toHaveClass('grid-cols-2');
    expect(actionMenu).toHaveClass('app-overlay-menu');
    for (const actionItem of actionItems) {
      expect(actionItem.querySelector('svg')).not.toBeNull();
    }

    expect(screen.getByText('preview.artifact.actions.star')).toBeInTheDocument();
    expect(screen.getByText('preview.artifact.actions.hide')).toBeInTheDocument();
    expect(screen.getByText('preview.artifact.actions.provenance')).toBeInTheDocument();
    expect(screen.getByText('preview.artifact.actions.versionHistory')).toBeInTheDocument();
    expect(screen.getByText('preview.artifact.actions.copyLink')).toBeInTheDocument();
    expect(screen.getByText('preview.artifact.actions.exportMetadata')).toBeInTheDocument();
    expect(screen.getByText('preview.artifact.exportToCloud')).toBeInTheDocument();

    await clickAndFlush(screen.getByText('preview.artifact.actions.star'));
    await waitFor(() =>
      expect(mocks.setPriority).toHaveBeenCalledWith({ artifactId: 'artifact-1', priority: 'user_starred' })
    );
    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    await clickAndFlush(screen.getByText('preview.artifact.actions.unstar'));
    await waitFor(() =>
      expect(mocks.setPriority).toHaveBeenLastCalledWith({ artifactId: 'artifact-1', priority: 'user_no_priority' })
    );

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    await clickAndFlush(screen.getByText('preview.artifact.actions.hide'));
    await waitFor(() =>
      expect(mocks.setPriority).toHaveBeenLastCalledWith({ artifactId: 'artifact-1', priority: 'user_hidden' })
    );
    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    await clickAndFlush(screen.getByText('preview.artifact.actions.unhide'));
    await waitFor(() =>
      expect(mocks.setPriority).toHaveBeenLastCalledWith({ artifactId: 'artifact-1', priority: 'user_no_priority' })
    );

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    await clickAndFlush(screen.getByText('preview.artifact.actions.copyLink'));
    await waitFor(() => expect(mocks.copyText).toHaveBeenCalledWith(expect.stringContaining('#/artifacts/artifact-1')));

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    await clickAndFlush(screen.getByText('preview.artifact.actions.exportMetadata'));
    await waitFor(() =>
      expect(mocks.download).toHaveBeenCalledWith(
        '/api/artifacts/artifact-1?include_metadata=true',
        'report.md.metadata.json'
      )
    );

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.addToChat'));
    expect(mocks.addToChat).toHaveBeenCalledWith({
      filename: 'report.md',
      artifactId: 'artifact-1',
      versionId: 'version-1',
      conversationId: 'frame-1',
    });

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.artifact.actions.viewInContext'));
    expect(mocks.navigate).toHaveBeenCalledWith('/conversation/frame-1');
    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.artifact.actions.provenance'));
    expect(mocks.navigate).toHaveBeenCalledWith('/artifacts/artifact-1?tab=lineage');
    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.artifact.actions.versionHistory'));
    expect(mocks.navigate).toHaveBeenCalledWith('/artifacts/artifact-1?tab=versions');
    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.artifact.actions.details'));
    expect(mocks.navigate).toHaveBeenCalledWith('/artifacts/artifact-1');
  });

  it('wires rename, copy, move, cloud export and destructive confirmation', async () => {
    const onRenamed = vi.fn();
    const onDeleted = vi.fn();
    render(
      <PreviewArtifactActions
        artifactId='artifact-1'
        versionId='version-1'
        fileName='report.md'
        onRenamed={onRenamed}
        onDeleted={onDeleted}
      />
    );
    await waitFor(() => expect(mocks.loadArtifact).toHaveBeenCalled());

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.artifact.actions.rename'));
    expect(screen.queryByRole('menu', { name: 'preview.artifact.actions.more' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('preview.artifact.actions.fileName'), { target: { value: 'renamed.md' } });
    await clickAndFlush(screen.getByTestId('modal-ok'));
    await waitFor(() =>
      expect(mocks.rename).toHaveBeenCalledWith({ artifactId: 'artifact-1', filename: 'renamed.md' })
    );
    expect(onRenamed).toHaveBeenCalledWith('renamed.md');

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.artifact.copyFile'));
    expect(screen.queryByRole('menu', { name: 'preview.artifact.actions.more' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('preview.artifact.copiedFilename'), { target: { value: 'copy.md' } });
    await clickAndFlush(screen.getByTestId('modal-ok'));
    await waitFor(() =>
      expect(mocks.copyArtifact).toHaveBeenCalledWith({ artifactId: 'artifact-1', newFilename: 'copy.md' })
    );

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    await clickAndFlush(screen.getByText('preview.artifact.moveToFolder'));
    expect(screen.queryByRole('menu', { name: 'preview.artifact.actions.more' })).not.toBeInTheDocument();
    await waitFor(() => expect(mocks.loadFolders).toHaveBeenCalledWith('project-1'));
    fireEvent.change(screen.getByLabelText('preview.artifact.targetFolder'), { target: { value: 'folder-1' } });
    await clickAndFlush(screen.getByTestId('modal-ok'));
    await waitFor(() =>
      expect(mocks.moveArtifact).toHaveBeenCalledWith({ artifactId: 'artifact-1', folderId: 'folder-1' })
    );

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    await clickAndFlush(screen.getByText('preview.artifact.exportToCloud'));
    expect(screen.queryByRole('menu', { name: 'preview.artifact.actions.more' })).not.toBeInTheDocument();
    await waitFor(() => expect(mocks.loadBuckets).toHaveBeenCalledWith('cloud-1'));
    await clickAndFlush(screen.getByTestId('modal-ok'));
    await waitFor(() =>
      expect(mocks.exportCloud).toHaveBeenCalledWith('cloud-1', {
        artifactId: 'artifact-1',
        bucket: 'bucket-1',
        key: 'report.md',
      })
    );

    fireEvent.click(screen.getByTestId('preview-artifact-actions-trigger'));
    fireEvent.click(screen.getByText('preview.artifact.actions.delete'));
    expect(screen.queryByRole('menu', { name: 'preview.artifact.actions.more' })).not.toBeInTheDocument();
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    await clickAndFlush(screen.getByTestId('delete-confirm'));
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith('artifact-1'));
    expect(onDeleted).toHaveBeenCalled();
  });
});
