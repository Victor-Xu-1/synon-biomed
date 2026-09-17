/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * Frontend think tag filter
 * Filters think tags from message content before rendering
 * This handles historical messages that were saved before the filter was implemented
 */

/**
 * Strip think tags from content
 * @param content - The content to filter
 * @returns Filtered content without think tags
 */
export function stripThinkTags(content: string): string {
  if (!content || typeof content !== 'string') {
    return content;
  }

  if (!hasThinkTags(content)) {
    return content;
  }

  return (
    content
      // Step 1: Remove complete <think>...</think> blocks (with optional spaces in tags)
      .replace(
        /<\s*think(?:_[a-z0-9][a-z0-9_-]{0,127})?\s*>([\s\S]*?)<\s*\/\s*think(?:_[a-z0-9][a-z0-9_-]{0,127})?\s*>/gi,
        ''
      )
      // Step 2: Remove complete <thinking>...</thinking> blocks (with optional spaces in tags)
      .replace(/<\s*thinking\s*>([\s\S]*?)<\s*\/\s*thinking\s*>/gi, '')
      // Step 3: A provider may append a unique `think_never_used_*` sentinel
      // after an ordinary public text block. It explicitly means that no
      // hidden-thinking block was opened, so remove only the sentinel. Treating
      // it like MiniMax's canonical orphaned `</think>` deletes the whole public
      // progress message and makes the transcript appear to retract itself.
      .replace(/<\s*\/\s*think_never_used_[a-z0-9][a-z0-9_-]{0,127}\s*>/gi, '')
      // Step 4: Handle the documented MiniMax-style canonical orphaned close:
      // "thinking content...\n</think>\nresponse". Only the unsuffixed tags
      // carry that prefix-is-hidden contract.
      .replace(/^[\s\S]*?<\s*\/\s*think(?:ing)?\s*>/i, '')
      // Step 5: Remove any remaining orphaned closing tags (just the tags, preserve surrounding content)
      // When text gets concatenated across tool calls, there may be additional </think> tags
      .replace(/<\s*\/\s*think(?:ing|_[a-z0-9][a-z0-9_-]{0,127})?\s*>/gi, '')
      // Step 6: Remove any remaining orphaned opening tags
      .replace(/<\s*think(?:ing|_[a-z0-9][a-z0-9_-]{0,127})?\s*>/gi, '')
      // Step 7: Collapse multiple newlines
      .replace(/\n{3,}/g, '\n\n')
  );
}

/**
 * Check if content contains think tags (opening or closing)
 * Also detects orphaned closing tags like </think> without opening <think>
 * @param content - The content to check
 * @returns True if think tags are present
 */
export function hasThinkTags(content: string): boolean {
  if (!content || typeof content !== 'string') {
    return false;
  }
  return /<\s*\/?\s*think(?:ing|_[a-z0-9][a-z0-9_-]{0,127})?\s*>/i.test(content);
}

/**
 * Filter think tags from message content object
 * Handles various message content structures
 * @param content - The message content (string or object)
 * @returns Filtered content
 */
export function filterMessageContent(content: string): string;
export function filterMessageContent<T>(content: T): T;
export function filterMessageContent(content: unknown): unknown {
  // Handle string content
  if (typeof content === 'string') {
    return hasThinkTags(content) ? stripThinkTags(content) : content;
  }

  // Handle object with content property
  if (content && typeof content === 'object' && 'content' in content) {
    const innerContent = content.content;
    if (typeof innerContent === 'string' && hasThinkTags(innerContent)) {
      return {
        ...content,
        content: stripThinkTags(innerContent),
      };
    }
  }

  return content;
}
