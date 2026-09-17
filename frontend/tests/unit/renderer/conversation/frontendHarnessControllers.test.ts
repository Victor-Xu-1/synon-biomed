import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const source = (relative: string): string =>
  readFileSync(fileURLToPath(new URL(`../../../../packages/desktop/src/${relative}`, import.meta.url)), 'utf8');

const sourcePath = (relative: string): string =>
  fileURLToPath(new URL(`../../../../packages/desktop/src/${relative}`, import.meta.url));

describe('frontend Harness controller boundaries', () => {
  it('keeps ipcBridge as a stable facade over responsibility-specific clients', () => {
    const facade = source('common/adapter/ipcBridge.ts');
    for (const moduleName of [
      'ipcBridgeTypes',
      'ipcCoreClients',
      'ipcConversationClient',
      'ipcRuntimeClient',
      'ipcFileClient',
      'ipcProviderClient',
      'ipcDatabaseClient',
      'ipcPreviewClient',
      'ipcOperationsClient',
    ]) {
      expect(facade).toContain(`export * from './${moduleName}'`);
      expect(existsSync(sourcePath(`common/adapter/${moduleName}.ts`))).toBe(true);
    }
    expect(facade).not.toContain('httpPost<');
    expect(facade).not.toContain('bridge.buildProvider');
  });

  it('separates composer, message projection and runtime presenters from their stable surfaces', () => {
    const sendBox = source('renderer/pages/conversation/platforms/acp/AcpSendBox.tsx');
    expect(sendBox).toContain('useAcpSendBoxDraftController');
    expect(sendBox).toContain('useAcpMobileActionSheetController');
    expect(sendBox).toContain('useConversationCommandQueue');

    const sharedSendBox = source('renderer/components/chat/SendBox/index.tsx');
    for (const moduleName of ['SendBoxComposerOverlays', 'SendBoxContextPreviews']) {
      expect(sharedSendBox).toContain(moduleName);
      expect(existsSync(sourcePath(`renderer/components/chat/SendBox/${moduleName}.tsx`))).toBe(true);
    }
    for (const moduleName of [
      'useComposerReferenceCatalog',
      'useSendBoxHistoryNavigation',
      'useSendBoxReferenceSelection',
    ]) {
      expect(sharedSendBox).toContain(moduleName);
      expect(existsSync(sourcePath(`renderer/components/chat/SendBox/${moduleName}.ts`))).toBe(true);
    }
    expect(sharedSendBox).not.toContain('fetchedArtifactProjectRef');
    expect(sharedSendBox).not.toContain('historyDraftRef');

    const computeSettings = source('renderer/pages/settings/ComputeSettings.tsx');
    for (const moduleName of [
      'ComputeJobs',
      'ComputeProviderModals',
      'ComputeProviders',
      'ComputeSettingsPrimitives',
    ]) {
      expect(computeSettings).toContain(moduleName);
      expect(existsSync(sourcePath(`renderer/pages/settings/components/compute/${moduleName}.tsx`))).toBe(true);
    }
    expect(computeSettings).not.toContain('const ComputeJobDetailModal');
    expect(computeSettings).not.toContain('const EditProviderModal');

    const messageList = source('renderer/pages/conversation/Messages/MessageList.tsx');
    expect(messageList).toContain('buildMessagePresentationList');
    expect(messageList).not.toContain('const pushToolList =');

    const runtimeFacade = source('renderer/components/synonBiomed/runtime/SynonBiomedRuntimeOperations.tsx');
    expect(runtimeFacade).toContain("export { default } from './SynonBiomedRuntimeOperationsController'");
    const runtimeController = source(
      'renderer/components/synonBiomed/runtime/SynonBiomedRuntimeOperationsController.tsx'
    );
    expect(runtimeController).toContain('SynonBiomedRuntimeStatusSurface');
    expect(runtimeController).toContain('SynonBiomedRuntimeDrawers');
    expect(runtimeController).not.toContain('SynonBiomedPlanReviewContent');
    const runtimeDrawers = source('renderer/components/synonBiomed/runtime/SynonBiomedRuntimeDrawers.tsx');
    expect(runtimeDrawers).toContain('SynonBiomedPlanReviewContent');
    expect(runtimeDrawers).toContain('SynonBiomedReviewFindingsContent');
    const runtimeStatus = source('renderer/components/synonBiomed/runtime/SynonBiomedRuntimeStatusSurface.tsx');
    expect(runtimeStatus).toContain('SynonBiomedTaskStatus');
    expect(runtimeStatus).toContain('AskUserCard');
    expect(existsSync(sourcePath('renderer/components/synonBiomed/runtime/runtimeOperationsControllerModel.ts'))).toBe(
      true
    );

    for (const moduleName of [
      'conversationStreamingApi',
      'conversationStreamingModel',
      'conversationTimelineToolOutput',
      'conversationToolStdoutStore',
      'useConversationStreaming',
    ]) {
      expect(existsSync(sourcePath(`renderer/services/runtime/${moduleName}.ts`))).toBe(true);
    }
    for (const legacyPath of [
      'renderer/components/synonBiomed/runtime/streamingModel.ts',
      'renderer/components/synonBiomed/runtime/streamingMessagesModel.ts',
      'renderer/components/synonBiomed/runtime/useSynonBiomedToolStdout.ts',
      'renderer/services/runtime/synonBiomedToolStdoutStore.ts',
      'renderer/services/synonBiomedStreaming.ts',
      'renderer/services/runtime/conversationStreamingProjection.ts',
    ]) {
      expect(existsSync(sourcePath(legacyPath))).toBe(false);
    }

    const acpMessage = source('renderer/pages/conversation/platforms/acp/useAcpMessage.ts');
    expect(acpMessage).toContain('useConversationStreaming');
    expect(acpMessage).toContain('mergeConversationToolOutput');
    expect(acpMessage).toContain('responseStream.on(handleResponseMessage)');
    expect(acpMessage).not.toContain('SynonBiomedStreamingProjection');
    const conversationSync = source('renderer/pages/conversation/platforms/acp/useSynonBiomedConversationSync.ts');
    expect(conversationSync).not.toContain('onStreamingProjection');
    expect(conversationSync).not.toContain('streaming-batch');
  });
});
