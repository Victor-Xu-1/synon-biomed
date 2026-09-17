import { askSynonBiomedAsideQuestion } from '@/renderer/services/synonBiomedAside';
import { Message } from '@arco-design/web-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

type BtwCommandState = {
  answer: string;
  isLoading: boolean;
  isOpen: boolean;
  question: string;
};

const INITIAL_STATE: BtwCommandState = {
  answer: '',
  isLoading: false,
  isOpen: false,
  question: '',
};

export function useBtwCommand(conversation_id?: string, enabled = true) {
  const { t } = useTranslation();
  const requestIdRef = useRef(0);
  const requestControllerRef = useRef<AbortController | null>(null);
  const previousConversationIdRef = useRef(conversation_id);
  const previousEnabledRef = useRef(enabled);
  const [state, setState] = useState<BtwCommandState>(INITIAL_STATE);

  const dismiss = useCallback(() => {
    requestIdRef.current += 1;
    requestControllerRef.current?.abort();
    requestControllerRef.current = null;
    setState(INITIAL_STATE);
  }, []);

  useEffect(() => () => requestControllerRef.current?.abort(), []);

  useEffect(() => {
    const conversationChanged = previousConversationIdRef.current !== conversation_id;
    const eligibilityDisabled = previousEnabledRef.current && !enabled;

    previousConversationIdRef.current = conversation_id;
    previousEnabledRef.current = enabled;

    if ((conversationChanged || eligibilityDisabled) && state.isOpen) {
      requestIdRef.current += 1;
      requestControllerRef.current?.abort();
      requestControllerRef.current = null;
      setState(INITIAL_STATE);
    }
  }, [conversation_id, enabled, state.isOpen]);

  const ask = useCallback(
    async (question: string) => {
      const requestId = ++requestIdRef.current;
      requestControllerRef.current?.abort();
      const controller = new AbortController();
      requestControllerRef.current = controller;
      Message.info(t('conversation.sideQuestion.started'));
      setState({
        answer: '',
        isLoading: true,
        isOpen: true,
        question,
      });

      if (!conversation_id) {
        Message.warning(t('conversation.sideQuestion.unsupported'));
        setState({
          answer: t('conversation.sideQuestion.unsupported'),
          isLoading: false,
          isOpen: true,
          question,
        });
        return;
      }

      try {
        const response = await askSynonBiomedAsideQuestion(conversation_id, question, { signal: controller.signal });

        if (requestId !== requestIdRef.current) {
          return;
        }

        const statusMap: Record<string, { toast: typeof Message.info; key: string }> = {
          ok: { toast: Message.success, key: 'answered' },
          noAnswer: { toast: Message.success, key: 'noAnswer' },
          unsupported: { toast: Message.warning, key: 'unsupported' },
          toolsRequired: { toast: Message.info, key: 'toolsRequired' },
          invalid: { toast: Message.warning, key: 'emptyQuestion' },
        };

        const entry = statusMap[response.status];
        if (entry) {
          const text =
            response.status === 'ok' && 'answer' in response
              ? response.answer
              : t(`conversation.sideQuestion.${entry.key}` as Parameters<typeof t>[0]);
          entry.toast(response.status === 'ok' ? t('conversation.sideQuestion.answered') : text);
          setState({ answer: text, isLoading: false, isOpen: true, question });
          return;
        }
      } catch (error) {
        if (requestId !== requestIdRef.current) {
          return;
        }
        if (controller.signal.aborted || (error instanceof DOMException && error.name === 'AbortError')) return;
        Message.error(t('conversation.sideQuestion.error'));
        setState({
          answer: t('conversation.sideQuestion.error'),
          isLoading: false,
          isOpen: true,
          question,
        });
      } finally {
        if (requestControllerRef.current === controller) requestControllerRef.current = null;
      }
    },
    [conversation_id, t]
  );

  return {
    ask,
    dismiss,
    ...state,
  };
}
