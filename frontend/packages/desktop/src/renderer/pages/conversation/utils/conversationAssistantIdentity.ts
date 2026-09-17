import type { TChatConversation } from '@/common/config/storage';
import type { PresetAssistantInfo } from '@/renderer/hooks/synonBiomed/runtime/usePresetAssistantInfo';
import { resolveAssistantAvatar } from '@/renderer/utils/model/assistantAvatar';
import { resolveAgentLogo } from '@/renderer/utils/synonBiomed/runtime/runtimeLogo';
import type { AgentLogoMap } from '@/renderer/utils/synonBiomed/runtime/runtimeLogo';
import { isRobotAvatar } from '@/renderer/components/synonBiomed/SynonBiomedAvatar';

/**
 * Resolve the effective Synon Biomed runtime backend for a conversation.
 *
 * Synon Biomed assistant-led flows pass the assistant backend explicitly when known.
 * Active ACP conversations may still fall back to extra.backend.
 */
export function resolveConversationBackend(
  conversation: TChatConversation | undefined,
  presetAssistantBackend?: string
): string | undefined {
  const explicitAssistantBackend = presetAssistantBackend?.trim();
  if (explicitAssistantBackend) {
    return explicitAssistantBackend;
  }

  if (!conversation) return undefined;

  const conversationAssistantBackend = conversation.assistant?.backend?.trim();
  if (conversationAssistantBackend) {
    return conversationAssistantBackend;
  }

  if (conversation.type === 'acp') {
    return conversation.extra?.backend;
  }

  return undefined;
}

export type ConversationLeadingMark =
  | {
      kind: 'emoji';
      value: string;
      label: string;
    }
  | {
      kind: 'image';
      value: string;
      label: string;
    }
  | {
      kind: 'fallback';
      label: string;
    }
  | {
      kind: 'assistant_fallback';
      label: string;
    };

export function resolveConversationLeadingMark(
  conversation: TChatConversation,
  assistantInfo: PresetAssistantInfo | undefined,
  logos: AgentLogoMap
): ConversationLeadingMark {
  if (assistantInfo) {
    if (assistantInfo.isFallback || (assistantInfo.isEmoji && isRobotAvatar(assistantInfo.logo))) {
      return {
        kind: 'assistant_fallback',
        label: assistantInfo.name,
      };
    }

    return assistantInfo.isEmoji && !isRobotAvatar(assistantInfo.logo)
      ? {
          kind: 'emoji',
          value: assistantInfo.logo,
          label: assistantInfo.name,
        }
      : {
          kind: 'image',
          value: assistantInfo.logo,
          label: assistantInfo.name,
        };
  }

  if (conversation.assistant) {
    const assistantLabel = conversation.assistant.name.trim() || conversation.assistant.id;
    const assistantAvatar = resolveAssistantAvatar(conversation.assistant.avatar);
    if (assistantAvatar.kind === 'emoji' && !isRobotAvatar(assistantAvatar.value)) {
      return {
        kind: 'emoji',
        value: assistantAvatar.value,
        label: assistantLabel,
      };
    }
    if (assistantAvatar.kind === 'image') {
      return {
        kind: 'image',
        value: assistantAvatar.value,
        label: assistantLabel,
      };
    }

    return {
      kind: 'assistant_fallback',
      label: assistantLabel,
    };
  }

  const backendKey = resolveConversationBackend(conversation)?.trim() || 'agent';
  const logo = resolveAgentLogo(logos, { backend: backendKey });
  if (logo) {
    return {
      kind: 'image',
      value: logo,
      label: backendKey,
    };
  }

  return {
    kind: 'fallback',
    label: backendKey,
  };
}
