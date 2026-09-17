const MAX_INLINE_IMAGE_BYTES = 8 * 1024 * 1024;
const INLINE_IMAGE_PREFIX = /^data:image\/(?:avif|gif|jpeg|png|webp);base64,/i;
const BASE64_PAYLOAD = /^[a-z0-9+/]*={0,2}$/i;

/**
 * Allow bounded raster data URLs produced by model and tool image blocks.
 * SVG is intentionally excluded because it can contain active content.
 */
export function isSafeInlineMarkdownImageUrl(value: string): boolean {
  const prefix = value.match(INLINE_IMAGE_PREFIX)?.[0];
  if (!prefix) return false;
  const payload = value.slice(prefix.length);
  if (!payload || !BASE64_PAYLOAD.test(payload)) return false;
  const maximumEncodedLength = Math.ceil(MAX_INLINE_IMAGE_BYTES / 3) * 4;
  return payload.length <= maximumEncodedLength;
}
