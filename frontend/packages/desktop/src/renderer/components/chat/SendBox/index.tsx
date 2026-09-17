/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { resolveSendBoxPrimaryAction } from './actionModel';
import { markComposerReferenceSinkActive } from './composerReferenceBridge';
import {
  createWorkspaceComposerReferences,
  filterComposerReferences,
  getActiveComposerReferenceQuery,
  type ArtifactComposerReference,
} from './composerReferenceModel';
import { useInputFocusRing } from '@/renderer/hooks/chat/useInputFocusRing';
import type { SlashCommandMenuItem } from '@/renderer/components/chat/SlashCommandMenu';
import { useBtwCommand } from '@/renderer/components/chat/BtwOverlay/useBtwCommand';
import { getFuzzyMatchIndices, useSlashCommandController } from '@/renderer/hooks/chat/useSlashCommandController';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { useConversationContextSafe } from '@/renderer/hooks/context/ConversationContext';
import { usePreviewContext } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import { getAllAtFileQueries } from '@/renderer/utils/chat/atFileQuery';
import { getLastAssistantText } from '@/renderer/utils/chat/getLastAssistantText';
import { type ReplyQuote, useAddEventListener } from '@/renderer/utils/emitter';
import type { FileSelectionItem } from '@/renderer/utils/file/fileSelection';
import { copyText } from '@/renderer/utils/ui/clipboard';
import { blurActiveElement, shouldBlockMobileInputFocus } from '@/renderer/utils/ui/focus';
import { Button, Input, Message } from '@arco-design/web-react';
import { ArrowUp, Plus } from '@icon-park/react';
import type { SlashCommandItem } from '@/common/chat/slash/types';
import { buildSkillSlashCommands, mergeSlashCommands } from '@/common/chat/slash/mergeSlashCommands';
import { theme } from '@office-ai/platform';
import React, { useCallback, useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import useSWR from 'swr';
import { useCompositionInput } from '@renderer/hooks/chat/useCompositionInput';
import { useConversationExport } from '@renderer/hooks/file/useConversationExport';
import { useDragUpload } from '@renderer/hooks/file/useDragUpload';
import { useLatestRef } from '@renderer/hooks/ui/useLatestRef';
import { usePasteService } from '@renderer/hooks/file/usePasteService';
import { useMessageList } from '@renderer/pages/conversation/Messages/hooks';
import type { FileMetadata } from '@renderer/services/FileService';
import { useUploadState } from '@renderer/hooks/file/useUploadState';
import { useAbortUploadsOnConversationChange } from '@renderer/hooks/file/useAbortUploadsOnConversationChange';
import UploadProgressBar from '@renderer/components/media/UploadProgressBar';
import { allSupportedExts } from '@renderer/services/FileService';
import SpeechInputButton from '@/renderer/components/chat/SpeechInputButton';
import { loadAvailableSkillsWithSynonBiomed } from '@/renderer/services/skills/skillsCatalog';
import { appendSpeechTranscript } from '@/renderer/hooks/system/useSpeechInput';
import { createChainedDispatch, useLiveTranscriptInsertion } from '@/renderer/hooks/system/useLiveTranscriptInsertion';
import { getConversationInputHistory } from '@/renderer/utils/chat/messageHistory';
import './sendbox.css';
import { extractBtwQuestion, getSelectedItemMatchKeys } from './selectionModel';
import { useSendBoxHistoryNavigation } from './useSendBoxHistoryNavigation';
import { useComposerReferenceCatalog } from './useComposerReferenceCatalog';
import { useSendBoxReferenceSelection } from './useSendBoxReferenceSelection';
import SendBoxComposerOverlays from './SendBoxComposerOverlays';
import SendBoxContextPreviews from './SendBoxContextPreviews';

const constVoid = (): void => undefined;
// 临界值：超过该字符数直接切换至多行模式，避免为超长文本做昂贵的宽度测量
// Threshold: switch to multi-line mode directly when character count exceeds this value to avoid heavy layout work
const MAX_SINGLE_LINE_CHARACTERS = 800;
const AT_FILE_HIGHLIGHT_COLOR = theme.Color.PrimaryColor;

const SendBox: React.FC<{
  value?: string;
  onChange?: (value: string) => void;
  onSend: (message: string) => Promise<void>;
  onStop?: () => Promise<void>;
  disabled?: boolean;
  loading?: boolean;
  className?: string;
  tools?: React.ReactNode;
  rightTools?: React.ReactNode;
  sendButtonSuffix?: React.ReactNode;
  prefix?: React.ReactNode;
  placeholder?: string;
  onFilesAdded?: (files: FileMetadata[]) => void;
  onBrowserFilesAdded?: (files: File[]) => void | Promise<void>;
  supportedExts?: string[];
  defaultMultiLine?: boolean;
  lockMultiLine?: boolean;
  sendButtonPrefix?: React.ReactNode;
  slash_commands?: SlashCommandItem[];
  onSlashBuiltinCommand?: (name: string) => void;
  onSelectSkillCommand?: (name: string) => void;
  onSelectArtifactReference?: (reference: ArtifactComposerReference) => void;
  hasPendingAttachments?: boolean;
  hasPendingContext?: boolean;
  enableBtw?: boolean;
  allowSendWhileLoading?: boolean;
  compactActions?: boolean;
  selectedWorkspaceItems?: FileSelectionItem[];
  onSelectedWorkspaceItemsChange?: (items: FileSelectionItem[]) => void;
  bottomHint?: React.ReactNode;
  /**
   * Mobile-only: open a parent-supplied action sheet via the `+` button.
   * When provided, mobile renders a single `+` button (left) and send/stop button (right);
   * `tools` and `rightTools` are not rendered inline on mobile.
   */
  onMobilePlusClick?: () => void;
}> = ({
  onSend,
  onStop,
  prefix,
  className,
  loading,
  tools,
  rightTools,
  sendButtonSuffix,
  disabled,
  placeholder,
  value: input = '',
  onChange: setInput = constVoid,
  onFilesAdded,
  onBrowserFilesAdded,
  supportedExts = allSupportedExts,
  defaultMultiLine = false,
  lockMultiLine = false,
  sendButtonPrefix,
  slash_commands = [],
  onSlashBuiltinCommand,
  onSelectSkillCommand,
  onSelectArtifactReference,
  hasPendingAttachments = false,
  hasPendingContext = false,
  enableBtw = false,
  allowSendWhileLoading = false,
  compactActions = false,
  selectedWorkspaceItems,
  onSelectedWorkspaceItemsChange,
  bottomHint,
  onMobilePlusClick,
}) => {
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  // Mobile compact mode: parent supplies the `+` action sheet, which collapses
  // tools/rightTools into a single launcher and lets the textarea start as a single line.
  const isMobileCompact = isMobile && Boolean(onMobilePlusClick);
  const effectiveLockMultiLine = lockMultiLine && !isMobileCompact;
  const effectiveDefaultMultiLine = defaultMultiLine && !isMobileCompact;
  const conversationContext = useConversationContextSafe();
  const { t } = useTranslation();
  const [isLoading, setIsLoading] = useState(false);
  const [isSingleLine, setIsSingleLine] = useState(!effectiveDefaultMultiLine);
  const [isInputFocused, setIsInputFocused] = useState(false);
  const isInputActive = isInputFocused;
  const { activeBorderColor, inactiveBorderColor, activeShadow, inactiveShadow, surfaceBackgroundColor } =
    useInputFocusRing();
  const containerRef = useRef<HTMLDivElement>(null);
  const singleLineWidthRef = useRef<number>(0);
  const measurementCanvasRef = useRef<HTMLCanvasElement | null>(null);
  const mobileUserFocusIntentUntilRef = useRef(0);
  const latestInputRef = useLatestRef(input);
  const setInputRef = useLatestRef(setInput);
  const messageList = useMessageList();
  const [replyQuote, setReplyQuote] = useState<ReplyQuote | null>(null);
  const [caretPosition, setCaretPosition] = useState(0);
  const [referenceMenuActiveIndex, setReferenceMenuActiveIndex] = useState(0);
  const [dismissedReferenceToken, setDismissedReferenceToken] = useState<string | null>(null);
  const highlightScrollRef = useRef<HTMLDivElement>(null);

  // Listen for reply events from message actions
  useAddEventListener('sendbox.reply', (quote) => setReplyQuote(quote), []);
  useAddEventListener('sendbox.reply.clear', () => setReplyQuote(null), []);

  // 集成预览面板的"添加到聊天"功能 / Integrate preview panel's "Add to chat" functionality
  const { setSendBoxHandler, domSnippets, removeDomSnippet, clearDomSnippets } = usePreviewContext();

  // 注册处理器以接收来自预览面板的文本 / Register handler to receive text from preview panel
  useEffect(() => {
    const handler = (text: string) => {
      const base = latestInputRef.current;
      const newValue = base ? `${base}\n\n${text}` : text;
      setInputRef.current(newValue);
    };
    setSendBoxHandler(handler);
    return () => {
      setSendBoxHandler(null);
    };
  }, [setSendBoxHandler]);

  // 初始化时获取单行输入框的可用宽度
  // Initialize and get the available width of single-line input
  useEffect(() => {
    const timer = setTimeout(() => {
      if (containerRef.current && singleLineWidthRef.current === 0) {
        const textarea = containerRef.current.querySelector('textarea');
        if (textarea) {
          // 保存单行模式下的可用宽度作为固定基准
          // Save the available width in single-line mode as a fixed baseline
          singleLineWidthRef.current = textarea.offsetWidth;
        }
      }
    }, 100);
    return () => clearTimeout(timer);
  }, []);

  // 移动端挂载后主动清除焦点，拦截路由切换导致的非用户触发聚焦
  useEffect(() => {
    if (!isMobile) return;
    const timer = setTimeout(() => {
      blurActiveElement();
    }, 0);
    return () => clearTimeout(timer);
  }, [isMobile]);

  // 检测是否单行
  // Detect whether to use single-line or multi-line mode
  useEffect(() => {
    // 有换行符直接多行
    // Switch to multi-line mode if newline character exists
    if (input.includes('\n')) {
      setIsSingleLine(false);
      return;
    }

    // 还没获取到基准宽度时不做判断
    // Skip detection if baseline width is not yet obtained
    if (singleLineWidthRef.current === 0) {
      return;
    }

    // 长文本无需测量，直接切换多行，防止创建超宽 DOM 触发长时间布局计算
    // Skip measurement for long text and switch to multi-line immediately to avoid expensive layout caused by extra-wide DOM
    if (input.length >= MAX_SINGLE_LINE_CHARACTERS) {
      setIsSingleLine(false);
      return;
    }

    // 检测内容宽度
    // Detect content width
    const frame = requestAnimationFrame(() => {
      const textarea = containerRef.current?.querySelector('textarea');
      if (!textarea) {
        return;
      }

      // 复用单个离屏 canvas，防止持续创建/销毁元素
      // Reuse a single offscreen canvas to avoid creating/destroying DOM nodes repeatedly
      const canvas = measurementCanvasRef.current ?? document.createElement('canvas');
      if (!measurementCanvasRef.current) {
        measurementCanvasRef.current = canvas;
      }
      const context = canvas.getContext('2d');
      if (!context) {
        return;
      }

      const textareaStyle = getComputedStyle(textarea);
      const fallbackFontSize = textareaStyle.fontSize || '14px';
      const fallbackFontFamily = textareaStyle.fontFamily || 'sans-serif';
      context.font = textareaStyle.font || `${fallbackFontSize} ${fallbackFontFamily}`.trim();

      const textWidth = context.measureText(input || '').width;

      // 使用初始化时保存的固定宽度作为判断基准
      // Use the fixed baseline width saved during initialization
      const baseWidth = singleLineWidthRef.current;

      // 文本宽度超过基准宽度时切换到多行
      // Switch to multi-line when text width exceeds baseline width
      if (textWidth >= baseWidth) {
        setIsSingleLine(false);
      } else if (textWidth < baseWidth - 30 && !effectiveLockMultiLine) {
        // 文本宽度小于基准宽度减30px时切回单行，留出小缓冲区避免临界点抖动
        // 如果 lockMultiLine 为 true，则不切换回单行
        // Switch back to single-line when text width is less than baseline minus 30px, leaving a small buffer to avoid flickering at the threshold
        // If lockMultiLine is true, do not switch back to single-line
        setIsSingleLine(true);
      }
      // 在 (baseWidth-30) 到 baseWidth 之间保持当前状态
      // Maintain current state between (baseWidth-30) and baseWidth
    });

    return () => cancelAnimationFrame(frame);
  }, [input, effectiveLockMultiLine]);

  // 使用拖拽 hook
  const { isFileDragging, dragHandlers } = useDragUpload({
    supportedExts,
    onFilesAdded,
    onBrowserFilesAdded,
    conversation_id: conversationContext?.conversation_id,
  });

  const { isUploading } = useUploadState('sendbox');
  // Bind sendbox uploads to the current conversation's lifecycle: switching
  // conversations or unmounting the SendBox aborts anything still in flight.
  useAbortUploadsOnConversationChange(conversationContext?.conversation_id, 'sendbox');
  const [message, context] = Message.useMessage();
  const conversationExport = useConversationExport({
    conversation_id: conversationContext?.conversation_id,
    workspace: conversationContext?.workspace,
    t,
    messageApi: message,
  });
  const btwCommand = useBtwCommand(conversationContext?.conversation_id, enableBtw);
  const btwQuestion = useMemo(() => extractBtwQuestion(input), [input]);
  const activeReferenceQuery = useMemo(() => {
    if (conversationContext?.projectId) {
      const sessionQuery = getActiveComposerReferenceQuery(input, caretPosition, '#');
      if (sessionQuery) return sessionQuery;
    }
    if (conversationContext?.workspace || conversationContext?.projectId) {
      return getActiveComposerReferenceQuery(input, caretPosition, '@');
    }
    return null;
  }, [caretPosition, conversationContext?.projectId, conversationContext?.workspace, input]);
  const activeReferenceTokenKey = useMemo(
    () =>
      activeReferenceQuery
        ? `${activeReferenceQuery.trigger}:${activeReferenceQuery.start}:${activeReferenceQuery.token}`
        : null,
    [activeReferenceQuery]
  );
  const atFileSessionKey = useMemo(() => {
    if (
      !conversationContext?.workspace ||
      conversationContext.workspace.startsWith('synonbiomed://') ||
      activeReferenceQuery?.trigger !== '@'
    ) {
      return null;
    }
    return `${conversationContext.workspace}:${activeReferenceQuery.start}`;
  }, [activeReferenceQuery, conversationContext?.workspace]);
  const allAtFileQueries = useMemo(() => getAllAtFileQueries(input), [input]);
  const deferredReferenceQuery = useDeferredValue(activeReferenceQuery?.query ?? '');
  const inputHistory = useMemo(
    () => getConversationInputHistory(messageList, conversationContext?.conversation_id),
    [conversationContext?.conversation_id, messageList]
  );
  const { clearHistoryNavigation, handleHistoryKeyDown, historyNavigationIndex } = useSendBoxHistoryNavigation({
    conversationId: conversationContext?.conversation_id,
    inputHistory,
    latestInputRef,
    setInputRef,
    containerRef,
  });
  const unmatchedSelectedWorkspaceItems = useMemo(() => {
    if (!selectedWorkspaceItems?.length) {
      return [];
    }

    const mentionQueries = new Set(allAtFileQueries.map((item) => item.query));
    return selectedWorkspaceItems.filter((item) => {
      if (typeof item !== 'string' && !item.isFile) {
        return false;
      }
      return !getSelectedItemMatchKeys(item).some((key) => mentionQueries.has(key));
    });
  }, [allAtFileQueries, selectedWorkspaceItems]);

  const builtinSlashCommands = useMemo<SlashCommandItem[]>(() => {
    const commands: SlashCommandItem[] = [];
    if (enableBtw) {
      commands.push({
        name: 'btw',
        description: t('conversation.sideQuestion.description'),
        kind: 'builtin',
        source: 'builtin',
        selectionBehavior: 'insert',
      });
    }
    if (onSlashBuiltinCommand) {
      commands.push({
        name: 'open',
        description: t('conversation.workspace.addFile'),
        kind: 'builtin',
        source: 'builtin',
      });
    }
    if (conversationContext?.conversation_id) {
      commands.push({
        name: 'copy',
        description: t('messages.copy'),
        kind: 'builtin',
        source: 'builtin',
      });
      // The `/export` slash command is intentionally not registered (kanban #14)
      // so it never appears in the command list and cannot open the export flow.
      // The `name === 'export'` branch below and the conversationExport hook are
      // kept intact for a future per-platform re-enable.
    }
    return commands;
  }, [conversationContext?.conversation_id, enableBtw, onSlashBuiltinCommand, t]);

  // Skills loaded into this conversation are also invokable via slash. We reuse
  // the global skills index (shared SWR key `skills-index`) purely to attach a
  // human-readable description; the loadedSkills snapshot decides which appear.
  const loadedSkills = conversationContext?.loadedSkills;
  const { data: skillIndex } = useSWR(loadedSkills && loadedSkills.length > 0 ? 'skills-index' : null, () =>
    loadAvailableSkillsWithSynonBiomed()
  );
  const skillSlashCommands = useMemo<SlashCommandItem[]>(() => {
    const descriptionByName = new Map((skillIndex ?? []).map((s) => [s.name, s.description]));
    return buildSkillSlashCommands(loadedSkills, descriptionByName, t('conversation.skills.slashHint'));
  }, [loadedSkills, skillIndex, t]);

  // Priority on name collisions: builtin > ACP agent commands > session skills.
  const mergedSlashCommands = useMemo(
    () => mergeSlashCommands(builtinSlashCommands, slash_commands, skillSlashCommands),
    [builtinSlashCommands, slash_commands, skillSlashCommands]
  );

  const slashController = useSlashCommandController({
    input,
    commands: mergedSlashCommands,
    onExecuteBuiltin: (name) => {
      if (name === 'copy') {
        const lastAssistantText = getLastAssistantText(messageList, Boolean(loading));
        if (!lastAssistantText) {
          Message.warning(t('messages.copyLastOutput.empty'));
        } else {
          void copyText(lastAssistantText)
            .then(() => {
              Message.success(t('messages.copySuccess'));
            })
            .catch(() => {
              Message.error(t('messages.copyFailed'));
            });
        }
      } else if (name === 'export') {
        void conversationExport.openExportFlow();
      } else {
        onSlashBuiltinCommand?.(name);
      }
      setInput('');
    },
    onSelectTemplate: (command) => {
      if (command.source === 'skill' && onSelectSkillCommand) {
        onSelectSkillCommand(command.name);
        setInput('');
        return;
      }
      setInput(`/${command.name} `);
    },
  });

  const slashMenuItems = useMemo<SlashCommandMenuItem[]>(
    () =>
      slashController.filteredCommands.map((command) => ({
        key: command.name,
        label: `/${command.name}`,
        description: command.description,
        badge: command.hint,
        highlightIndices: slashController.query
          ? getFuzzyMatchIndices(command.name, slashController.query)?.map((index) => index + 1)
          : undefined,
      })),
    [slashController.filteredCommands, slashController.query]
  );

  const isCommandMenuOpen = conversationExport.isOpen || slashController.isOpen;
  const isReferenceMenuOpen =
    Boolean(activeReferenceQuery) && activeReferenceTokenKey !== dismissedReferenceToken && !isCommandMenuOpen;
  const {
    artifactError: artifactReferencesError,
    artifactItems: artifactReferences,
    artifactLoading: artifactReferencesLoading,
    sessionError: sessionReferencesError,
    sessionItems: sessionReferences,
    sessionLoading: sessionReferencesLoading,
    workspaceError: workspaceMentionError,
    workspaceItems: workspaceMentionItems,
    workspaceLoading: workspaceMentionLoading,
  } = useComposerReferenceCatalog({
    conversationId: conversationContext?.conversation_id,
    currentFrameId: conversationContext?.currentFrameId,
    isOpen: isReferenceMenuOpen,
    projectId: conversationContext?.projectId,
    trigger: activeReferenceQuery?.trigger,
    workspace: conversationContext?.workspace,
    workspaceSessionKey: atFileSessionKey,
  });
  const workspaceReferences = useMemo(
    () => createWorkspaceComposerReferences(workspaceMentionItems),
    [workspaceMentionItems]
  );
  const referenceItems = useMemo(
    () => (activeReferenceQuery?.trigger === '@' ? [...artifactReferences, ...workspaceReferences] : sessionReferences),
    [activeReferenceQuery?.trigger, artifactReferences, sessionReferences, workspaceReferences]
  );
  const visibleReferenceItems = useMemo(
    () => filterComposerReferences(referenceItems, deferredReferenceQuery),
    [deferredReferenceQuery, referenceItems]
  );
  const referenceLoading =
    activeReferenceQuery?.trigger === '@'
      ? artifactReferencesLoading || workspaceMentionLoading
      : sessionReferencesLoading;
  const referenceError =
    activeReferenceQuery?.trigger === '@' ? artifactReferencesError || workspaceMentionError : sessionReferencesError;
  const isOverlayOpen = isCommandMenuOpen || btwCommand.isOpen || isReferenceMenuOpen;

  const getTextareaElement = useCallback((): HTMLTextAreaElement | null => {
    const textarea = containerRef.current?.querySelector('textarea');
    return textarea instanceof HTMLTextAreaElement ? textarea : null;
  }, []);

  const { insertSelectedReference, removeExternalSelection } = useSendBoxReferenceSelection({
    activeQuery: activeReferenceQuery,
    activeTokenKey: activeReferenceTokenKey,
    allAtFileQueries,
    conversationId: conversationContext?.conversation_id,
    conversationType: conversationContext?.type,
    getTextareaElement,
    input,
    latestInputRef,
    onInvalidReference: () => message.error(t('conversation.sendbox.invalidReference')),
    onSelectArtifactReference,
    onSelectedWorkspaceItemsChange,
    selectedWorkspaceItems,
    setCaretPosition,
    setDismissedReferenceToken,
    setInput,
    setInputRef,
  });

  const syncCaretPosition = useCallback(
    (target?: EventTarget | null) => {
      const textarea = target instanceof HTMLTextAreaElement ? target : getTextareaElement();
      if (!textarea) {
        return;
      }
      setCaretPosition(textarea.selectionStart ?? textarea.value.length);
    },
    [getTextareaElement]
  );

  const syncHighlightScroll = useCallback(
    (target?: EventTarget | null) => {
      const textarea = target instanceof HTMLTextAreaElement ? target : getTextareaElement();
      if (!textarea || !highlightScrollRef.current) {
        return;
      }
      highlightScrollRef.current.scrollTop = textarea.scrollTop;
      highlightScrollRef.current.scrollLeft = textarea.scrollLeft;
    },
    [getTextareaElement]
  );

  const syncHighlightTextMetrics = useCallback(
    (target?: EventTarget | null) => {
      const textarea = target instanceof HTMLTextAreaElement ? target : getTextareaElement();
      const highlightLayer = highlightScrollRef.current;
      if (!textarea || !highlightLayer) {
        return;
      }

      const textareaStyle = getComputedStyle(textarea);
      highlightLayer.style.direction = textareaStyle.direction;
      highlightLayer.style.fontFamily = textareaStyle.fontFamily;
      highlightLayer.style.fontSize = textareaStyle.fontSize;
      highlightLayer.style.fontStyle = textareaStyle.fontStyle;
      highlightLayer.style.fontWeight = textareaStyle.fontWeight;
      highlightLayer.style.letterSpacing = textareaStyle.letterSpacing;
      highlightLayer.style.lineHeight = textareaStyle.lineHeight;
      highlightLayer.style.paddingTop = textareaStyle.paddingTop;
      highlightLayer.style.paddingRight = textareaStyle.paddingRight;
      highlightLayer.style.paddingBottom = textareaStyle.paddingBottom;
      highlightLayer.style.paddingLeft = textareaStyle.paddingLeft;
      highlightLayer.style.tabSize = textareaStyle.tabSize;
      highlightLayer.style.textAlign = textareaStyle.textAlign;
      highlightLayer.style.textIndent = textareaStyle.textIndent;
      highlightLayer.style.textTransform = textareaStyle.textTransform;
      highlightLayer.style.wordSpacing = textareaStyle.wordSpacing;
    },
    [getTextareaElement]
  );

  useLayoutEffect(() => {
    syncHighlightTextMetrics();
  }, [input, isInputFocused, isMobile, isSingleLine, syncHighlightTextMetrics]);

  const handleTextAreaChange = (value: string) => {
    if (historyNavigationIndex !== null) {
      clearHistoryNavigation(false);
    }
    if (conversationExport.isOpen && value) {
      conversationExport.closeExportFlow();
    }
    setInput(value);
    requestAnimationFrame(() => {
      syncCaretPosition();
      syncHighlightScroll();
    });
  };

  const handleOverlayKeyDown = (event: React.KeyboardEvent) => {
    return conversationExport.handleKeyDown(event) || slashController.onKeyDown(event);
  };

  useEffect(() => {
    if (!activeReferenceTokenKey) {
      setReferenceMenuActiveIndex(0);
      return;
    }
    setReferenceMenuActiveIndex(0);
  }, [activeReferenceTokenKey]);

  useEffect(() => {
    if (!visibleReferenceItems.length) {
      setReferenceMenuActiveIndex(0);
      return;
    }
    setReferenceMenuActiveIndex((previous) => Math.min(previous, visibleReferenceItems.length - 1));
  }, [visibleReferenceItems]);

  // 使用共享的输入法合成处理
  const { compositionHandlers, isComposingState, createKeyDownHandler } = useCompositionInput();

  // 使用共享的PasteService集成
  const { onPaste, onFocus: handlePasteFocus } = usePasteService({
    supportedExts,
    onFilesAdded,
    onBrowserFilesAdded,
    conversation_id: conversationContext?.conversation_id,
    onTextPaste: (text: string) => {
      // 处理清理后的文本粘贴，在当前光标位置插入文本而不是替换整个内容
      const textarea = document.activeElement as HTMLTextAreaElement;
      if (textarea && textarea.tagName === 'TEXTAREA') {
        const cursorPosition = textarea.selectionStart;
        const current_value = textarea.value;
        const start = textarea.selectionStart ?? textarea.value.length;
        const end = textarea.selectionEnd ?? start;
        const newValue = current_value.slice(0, start) + text + current_value.slice(end);
        setInput(newValue);
        // 设置光标到插入文本后的位置
        setTimeout(() => {
          textarea.setSelectionRange(cursorPosition + text.length, cursorPosition + text.length);
        }, 0);
      } else {
        // 如果无法获取光标位置，回退到追加到末尾的行为
        setInput(text);
      }
    },
  });
  const markMobileFocusIntent = useCallback(() => {
    if (!isMobile) return;
    mobileUserFocusIntentUntilRef.current = Date.now() + 1500;
  }, [isMobile]);

  const handleInputFocus = useCallback(() => {
    if (conversationContext?.conversation_id) {
      markComposerReferenceSinkActive(conversationContext.conversation_id);
    }
    if (isMobile && Date.now() > mobileUserFocusIntentUntilRef.current) {
      blurActiveElement();
      return;
    }
    if (isMobile && shouldBlockMobileInputFocus()) {
      blurActiveElement();
      return;
    }
    mobileUserFocusIntentUntilRef.current = 0;
    handlePasteFocus();
    setIsInputFocused(true);
  }, [conversationContext?.conversation_id, handlePasteFocus, isMobile]);
  const handleInputBlur = useCallback(() => {
    setIsInputFocused(false);
  }, []);

  useEffect(() => {
    clearHistoryNavigation(false);
  }, [clearHistoryNavigation, conversationContext?.conversation_id]);

  const handleReferenceMenuKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (!isReferenceMenuOpen || !activeReferenceTokenKey) {
        return false;
      }

      if (event.key === 'Escape') {
        event.preventDefault();
        setDismissedReferenceToken(activeReferenceTokenKey);
        return true;
      }

      if (!visibleReferenceItems.length) {
        return false;
      }

      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setReferenceMenuActiveIndex((previous) => (previous + 1) % visibleReferenceItems.length);
        return true;
      }

      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setReferenceMenuActiveIndex((previous) => (previous === 0 ? visibleReferenceItems.length - 1 : previous - 1));
        return true;
      }

      if (event.key === 'Enter') {
        const selectedItem = visibleReferenceItems[referenceMenuActiveIndex];
        if (!selectedItem) {
          return false;
        }
        event.preventDefault();
        insertSelectedReference(selectedItem);
        return true;
      }

      return false;
    },
    [
      activeReferenceTokenKey,
      insertSelectedReference,
      isReferenceMenuOpen,
      referenceMenuActiveIndex,
      visibleReferenceItems,
    ]
  );

  const sendMessageHandler = () => {
    if (isUploading) return;
    if (enableBtw && btwQuestion !== null) {
      const normalizedQuestion = btwQuestion.trim();
      if (!normalizedQuestion) {
        message.warning(t('conversation.sideQuestion.emptyQuestion'));
        return;
      }
      if (btwCommand.isLoading) {
        message.warning(t('conversation.sideQuestion.alreadyRunning'));
        return;
      }
      if (hasPendingAttachments || hasPendingContext || domSnippets.length > 0) {
        message.warning(t('conversation.sideQuestion.attachmentsNotAllowed'));
        return;
      }
      clearHistoryNavigation(false);
      setInput('');
      void btwCommand.ask(normalizedQuestion);
      return;
    }

    if (!allowSendWhileLoading && (isLoading || loading)) {
      console.info('[sendbox]', {
        event: 'blocked-while-loading',
        allowSendWhileLoading,
        isLoading,
        loading,
      });
      message.warning(t('messages.conversationInProgress'));
      return;
    }
    if (!input.trim() && domSnippets.length === 0 && !hasPendingAttachments && !hasPendingContext) {
      return;
    }
    console.info('[sendbox]', {
      event: 'submit',
      allowSendWhileLoading,
      isLoading,
      loading,
      inputLength: input.length,
      domSnippetCount: domSnippets.length,
    });
    setIsLoading(true);
    clearHistoryNavigation(false);

    // 构建消息内容 / Build message content
    let finalMessage = input;

    // Prepend reply quote as blockquote
    if (replyQuote) {
      const quotedLines = replyQuote.content
        .split('\n')
        .map((line) => `> ${line}`)
        .join('\n');
      finalMessage = `${quotedLines}\n\n${finalMessage}`;
    }

    // 如果有 DOM 片段，附加完整 HTML / If has DOM snippets, append full HTML
    if (domSnippets.length > 0) {
      const snippetsHtml = domSnippets
        .map((s) => `\n\n---\nDOM Snippet (${s.tag}):\n\`\`\`html\n${s.html}\n\`\`\``)
        .join('');
      finalMessage = input + snippetsHtml;
    }

    // 立即清空输入框，避免异步 onSend 完成后覆盖用户新输入
    // Clear input immediately to prevent async onSend completion from overwriting new user input
    setInput('');
    clearDomSnippets();
    setReplyQuote(null);

    onSend(finalMessage)
      .catch(() => {})
      .finally(() => {
        setIsLoading(false);
      });
  };

  const stopHandler = async () => {
    if (!onStop) return;
    try {
      await onStop();
    } finally {
      setIsLoading(false);
    }
  };

  // SendBox is controlled (`value`/`onChange`): adapt the plain value setter
  // into a functional-update dispatch. Same-tick updates (live-region restore
  // followed by the terminal transcript append) must chain through a pending
  // value — the committed `input` prop only catches up on the next render.
  const speechDispatch = useMemo(
    () =>
      createChainedDispatch(
        () => latestInputRef.current,
        (value) => setInputRef.current(value)
      ),
    [latestInputRef, setInputRef]
  );
  useEffect(() => {
    // The committed input caught up (or the user typed) — drop the chain.
    speechDispatch.reset();
  }, [input, speechDispatch]);
  const handleSpeechTranscript = useCallback(
    (transcript: string) => {
      speechDispatch.dispatch((prev) => appendSpeechTranscript(prev, transcript));
    },
    [speechDispatch]
  );
  const { handleLiveTranscript } = useLiveTranscriptInsertion(speechDispatch.dispatch);

  const hasDraftToSend =
    input.trim().length > 0 || domSnippets.length > 0 || hasPendingAttachments || hasPendingContext;

  // Calculate button disabled state
  const isButtonDisabled =
    disabled ||
    isUploading ||
    (!input.trim() && domSnippets.length === 0 && !hasPendingAttachments && !hasPendingContext);

  // Reusable send button component
  const sendButton = (
    <Button
      shape='circle'
      type='primary'
      disabled={isButtonDisabled}
      className='send-button-custom'
      icon={<ArrowUp theme='filled' size='14' fill='white' strokeWidth={5} className='sendbox-action-icon' />}
      onClick={sendMessageHandler}
      data-testid='sendbox-send-btn'
      aria-label={t('common.send')}
    />
  );

  const stopButton = (
    <Button
      shape='circle'
      type='secondary'
      className='bg-animate sendbox-stop-button'
      icon={<div className='mx-auto size-12px bg-6'></div>}
      onClick={stopHandler}
      aria-label={t('conversation.synonRuntime.sendBox.stopTask')}
      title={t('conversation.synonRuntime.sendBox.stopTask')}
    ></Button>
  );

  const renderActionButtons = () => {
    const action = resolveSendBoxPrimaryAction({
      loading: isLoading || loading,
      hasStopHandler: Boolean(onStop),
      allowSendWhileLoading,
      compactActions,
      hasDraftToSend,
      disabled,
      uploading: isUploading,
    });
    return action === 'stop' ? stopButton : sendButton;
  };

  const renderSendActions = () => {
    if (isLoading || loading || !sendButtonSuffix) {
      return renderActionButtons();
    }
    return (
      <div className='sendbox-joined-actions'>
        {renderActionButtons()}
        <span className='sendbox-joined-actions__divider' aria-hidden='true' />
        {sendButtonSuffix}
      </div>
    );
  };

  const shouldUseHighlightOverlay = !isComposingState && allAtFileQueries.length > 0;

  const mobilePlusButton = isMobileCompact ? (
    <Button
      shape='circle'
      type='secondary'
      className='sendbox-mobile-plus-btn'
      icon={<Plus theme='outline' size='16' />}
      onClick={onMobilePlusClick}
      data-testid='sendbox-mobile-plus-btn'
      aria-label={t('common.more')}
    />
  ) : null;

  // On mobile compact mode, the parent supplies the action sheet for configuration
  // controls. Voice remains a first-class composer action, matching the v1.1
  // workbench instead of hiding microphone access behind the sheet.
  const renderedTools = isMobileCompact ? mobilePlusButton : tools;
  const renderedRightTools = isMobileCompact ? null : rightTools;
  const renderedSpeechButton = (
    <SpeechInputButton
      disabled={disabled || isUploading || (!allowSendWhileLoading && (isLoading || loading))}
      onLiveTranscript={handleLiveTranscript}
      onTranscript={handleSpeechTranscript}
    />
  );

  const renderHighlightedInputValue = useCallback(() => {
    if (!input) {
      return <span className='sendbox-highlight-text'>{'\u200b'}</span>;
    }

    const segments: React.ReactNode[] = [];
    let cursor = 0;

    allAtFileQueries.forEach((match, index) => {
      if (cursor < match.start) {
        segments.push(
          <span className='sendbox-highlight-text' key={`text-${cursor}`}>
            {input.slice(cursor, match.start)}
          </span>
        );
      }

      segments.push(
        <span
          className='sendbox-highlight-mention'
          key={`mention-${match.start}-${index}`}
          style={{ color: AT_FILE_HIGHLIGHT_COLOR }}
        >
          {input.slice(match.start, match.end)}
        </span>
      );
      cursor = match.end;
    });

    if (cursor < input.length) {
      segments.push(
        <span className='sendbox-highlight-text' key={`text-${cursor}`}>
          {input.slice(cursor)}
        </span>
      );
    }

    return segments;
  }, [allAtFileQueries, input]);

  return (
    <div className={className}>
      <div
        ref={containerRef}
        className={`sendbox-panel relative p-16px border-3 b bg-dialog-fill-0 b-solid rd-20px flex flex-col ${
          isOverlayOpen ? 'overflow-visible' : 'overflow-hidden'
        } ${isFileDragging ? 'b-dashed sendbox-panel--dragging' : ''}`}
        style={{
          backgroundColor: surfaceBackgroundColor,
          borderRadius: '20px',
          transition: 'box-shadow 0.2s ease, border-color 0.2s ease, background-color 0.16s ease',
          ...(isFileDragging
            ? {
                backgroundColor: 'var(--color-primary-light-1)',
                borderColor: 'rgb(var(--primary-3))',
                borderWidth: '1px',
              }
            : {
                borderWidth: '1px',
                borderColor: isInputActive ? activeBorderColor : inactiveBorderColor,
                boxShadow: isInputActive ? activeShadow : inactiveShadow,
              }),
        }}
        {...dragHandlers}
      >
        <SendBoxComposerOverlays
          activeReferenceQuery={activeReferenceQuery}
          anchorEl={containerRef.current}
          btwCommand={btwCommand}
          conversationExport={conversationExport}
          insertSelectedReference={insertSelectedReference}
          isCommandMenuOpen={isCommandMenuOpen}
          isLoading={isLoading}
          isReferenceMenuOpen={isReferenceMenuOpen}
          loading={loading}
          referenceError={referenceError}
          referenceLoading={referenceLoading}
          referenceMenuActiveIndex={referenceMenuActiveIndex}
          setReferenceMenuActiveIndex={setReferenceMenuActiveIndex}
          slashController={slashController}
          slashMenuItems={slashMenuItems}
          t={t}
          visibleReferenceItems={visibleReferenceItems}
        />
        <div style={{ width: '100%' }}>
          {prefix}
          {context}
          <SendBoxContextPreviews
            domSnippets={domSnippets}
            onClearReply={() => setReplyQuote(null)}
            onRemoveDomSnippet={removeDomSnippet}
            onRemoveExternalSelection={removeExternalSelection}
            replyQuote={replyQuote}
            unmatchedSelectedWorkspaceItems={onSelectedWorkspaceItemsChange ? unmatchedSelectedWorkspaceItems : []}
          />
        </div>
        <UploadProgressBar source='sendbox' />
        <div
          className={isSingleLine ? 'flex items-center gap-2 w-full min-w-0 overflow-hidden' : 'w-full overflow-hidden'}
        >
          {isSingleLine && (
            <div
              className={
                isMobileCompact
                  ? 'flex-shrink-0 sendbox-tools sendbox-tools-mobile-compact'
                  : isMobile
                    ? 'sendbox-tools sendbox-tools-scroll-mobile'
                    : 'flex-shrink-0 sendbox-tools'
              }
            >
              {renderedTools}
            </div>
          )}
          <div
            className={`sendbox-highlight-container ${isSingleLine ? 'sendbox-highlight-container--single' : ''}`}
            style={{
              width: isSingleLine ? 'auto' : '100%',
              flex: isSingleLine ? 1 : 'none',
              minWidth: 0,
              maxWidth: '100%',
              marginBottom: isSingleLine ? 0 : '8px',
              minHeight: isSingleLine ? '20px' : '40px',
            }}
          >
            <div
              ref={highlightScrollRef}
              aria-hidden='true'
              className={`sendbox-highlight-layer text-14px ${
                isMobile ? 'sendbox-input--mobile' : ''
              } ${isSingleLine ? 'sendbox-highlight-layer--single' : ''}`}
              data-testid='sendbox-highlight-layer'
              style={!shouldUseHighlightOverlay ? { visibility: 'hidden' } : undefined}
            >
              {renderHighlightedInputValue()}
            </div>
            <Input.TextArea
              autoFocus={!isMobile}
              disabled={disabled}
              spellCheck={false}
              value={input}
              placeholder={
                isMobileCompact
                  ? (placeholder ?? (bottomHint as string | undefined) ?? t('conversation.sendbox.hint'))
                  : placeholder
                    ? bottomHint === ''
                      ? placeholder
                      : `${placeholder}  ${bottomHint ?? t('conversation.sendbox.hint')}`
                    : ((bottomHint as string | undefined) ?? t('conversation.sendbox.hint'))
              }
              className={`${
                shouldUseHighlightOverlay ? 'sendbox-highlight-textarea ' : ''
              }pl-0 pr-0 !b-none focus:shadow-none m-0 !bg-transparent !focus:bg-transparent !hover:bg-transparent lh-[20px] !resize-none text-14px ${
                isMobile ? 'sendbox-input--mobile' : ''
              }`}
              data-testid='sendbox-input'
              style={{
                width: '100%',
                flex: isSingleLine ? 1 : 'none',
                minWidth: 0,
                maxWidth: '100%',
                marginLeft: 0,
                marginRight: 0,
                marginBottom: 0,
                height: isSingleLine ? (isMobile ? '22px' : '20px') : 'auto',
                minHeight: isSingleLine ? (isMobile ? '22px' : '20px') : '40px',
                overflowY: isSingleLine ? 'hidden' : 'auto',
                overflowX: 'hidden',
                whiteSpace: isSingleLine ? 'nowrap' : 'pre-wrap',
                textOverflow: isSingleLine ? 'ellipsis' : 'clip',
                wordBreak: isSingleLine ? 'normal' : 'break-word',
                overflowWrap: 'break-word',
              }}
              onChange={handleTextAreaChange}
              onPaste={onPaste}
              onTouchStart={markMobileFocusIntent}
              onMouseDown={markMobileFocusIntent}
              onClick={(event) => {
                syncCaretPosition(event.target);
              }}
              onFocus={handleInputFocus}
              onBlur={handleInputBlur}
              onKeyUp={(event) => {
                syncCaretPosition(event.currentTarget);
              }}
              onSelect={(event) => {
                syncCaretPosition(event.currentTarget);
              }}
              onScroll={(event) => {
                syncHighlightScroll(event.currentTarget);
              }}
              {...compositionHandlers}
              autoSize={isSingleLine ? false : { minRows: 1, maxRows: 10 }}
              onKeyDown={createKeyDownHandler(sendMessageHandler, (event) => {
                return handleReferenceMenuKeyDown(event) || handleOverlayKeyDown(event) || handleHistoryKeyDown(event);
              })}
            ></Input.TextArea>
          </div>
          {isSingleLine && (
            <div className='flex items-center gap-2'>
              {renderedRightTools}
              {renderedSpeechButton}
              {sendButtonPrefix}
              {renderSendActions()}
            </div>
          )}
        </div>
        {!isSingleLine && (
          <div className='flex items-center justify-between gap-2 w-full'>
            <div
              className={
                isMobileCompact
                  ? 'flex-shrink-0 sendbox-tools sendbox-tools-mobile-compact'
                  : isMobile
                    ? 'sendbox-tools sendbox-tools-scroll-mobile'
                    : 'sendbox-tools'
              }
            >
              {renderedTools}
            </div>
            <div className='sendbox-actions flex items-center gap-2'>
              {renderedRightTools}
              {renderedSpeechButton}
              {sendButtonPrefix}
              {renderSendActions()}
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

export default SendBox;
