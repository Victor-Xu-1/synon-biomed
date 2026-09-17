export type CompactDescriptionOptions = {
  maxLength?: number;
  stripPrefixes?: Array<string | undefined>;
};

export function compactSettingsDescription(
  description: string,
  { maxLength = 112, stripPrefixes = [] }: CompactDescriptionOptions = {}
): string {
  const normalized = description.trim().replace(/\s+/g, ' ');
  let value = normalized;

  for (const prefix of stripPrefixes) {
    if (!prefix || !value.toLocaleLowerCase().startsWith(prefix.toLocaleLowerCase())) continue;
    const candidate = value.slice(prefix.length).replace(/^[\s:：,，\-—]+/, '');
    if (candidate) value = candidate;
    break;
  }

  const punctuationEnd = value.search(/[。！？!?]/);
  const periodMatch = /\.(?:\s|$)/.exec(value);
  const periodEnd = periodMatch?.index ?? -1;
  const sentenceEnd = [punctuationEnd, periodEnd].reduce<number | undefined>(
    (earliest, index) =>
      index >= 0 && index < maxLength && (earliest === undefined || index < earliest) ? index : earliest,
    undefined
  );
  if (sentenceEnd !== undefined) value = value.slice(0, sentenceEnd + 1);

  const characters = Array.from(value);
  return characters.length > maxLength ? `${characters.slice(0, maxLength).join('').trimEnd()}…` : value;
}
