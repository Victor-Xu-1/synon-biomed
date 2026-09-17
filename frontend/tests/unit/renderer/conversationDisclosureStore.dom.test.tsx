import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import {
  conversationDisclosureStore,
  useConversationDisclosure,
} from '@/renderer/services/runtime/conversationDisclosureStore';

const identity = {
  conversationId: 'conversation-1',
  operationId: 'operation-1',
  path: 'result/records',
};

describe('conversation disclosure store', () => {
  beforeEach(() => conversationDisclosureStore.resetForTests());

  it('restores manual disclosure state after a component remount', () => {
    const first = renderHook(() => useConversationDisclosure(identity));
    expect(first.result.current.expanded).toBe(false);
    act(() => first.result.current.toggle());
    expect(first.result.current.expanded).toBe(true);
    first.unmount();

    const restored = renderHook(() => useConversationDisclosure(identity));
    expect(restored.result.current.expanded).toBe(true);
  });

  it('does not override a manual collapse when a detail defaults to expanded', () => {
    const first = renderHook(() => useConversationDisclosure(identity, { defaultExpanded: true }));
    expect(first.result.current.expanded).toBe(true);
    act(() => first.result.current.setExpanded(false));
    expect(first.result.current.expanded).toBe(false);
    first.unmount();

    const restored = renderHook(() => useConversationDisclosure(identity, { defaultExpanded: true }));
    expect(restored.result.current.expanded).toBe(false);
  });

  it('notifies mounted disclosures when an account-bound cache reset clears state', () => {
    const disclosure = renderHook(() => useConversationDisclosure(identity));
    act(() => disclosure.result.current.setExpanded(true));
    expect(disclosure.result.current.expanded).toBe(true);

    act(() => conversationDisclosureStore.clear());

    expect(disclosure.result.current.expanded).toBe(false);
  });

  it('keeps disclosure choices isolated between conversation branches', () => {
    const first = renderHook(() => useConversationDisclosure({ ...identity, branchId: 'br_11111111' }));
    act(() => first.result.current.setExpanded(true));
    expect(first.result.current.expanded).toBe(true);

    const second = renderHook(() => useConversationDisclosure({ ...identity, branchId: 'br_22222222' }));
    expect(second.result.current.expanded).toBe(false);
  });
});
