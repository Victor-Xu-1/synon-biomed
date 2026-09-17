/** Durable text publication coverage retained by the message, not a mounted subscriber. */
export type TextPublicationRange = readonly [number, number];

type Coordinates = {
  conversation_id: string;
  msg_id?: string;
  source_publication_sequence?: number;
  history_coverage_through?: number;
  text_publication_ranges?: readonly TextPublicationRange[];
};

const positiveSequence = (value: number | undefined): value is number =>
  Number.isSafeInteger(value) && Number(value) > 0;

const sameIdentity = (left: Coordinates, right: Coordinates): boolean =>
  !!left.msg_id && left.msg_id === right.msg_id && left.conversation_id === right.conversation_id;

function ranges(value: Coordinates): TextPublicationRange[] {
  const result = (value.text_publication_ranges ?? []).filter(
    ([start, end]) => positiveSequence(start) && positiveSequence(end) && start <= end
  );
  if (positiveSequence(value.source_publication_sequence)) {
    result.push([value.source_publication_sequence, value.source_publication_sequence]);
  }
  return result;
}

export function isTextPublicationCovered(existing: Coordinates, incoming: Coordinates): boolean {
  const sequence = incoming.source_publication_sequence;
  if (!sameIdentity(existing, incoming) || !positiveSequence(sequence)) return false;
  return (
    (positiveSequence(existing.history_coverage_through) && sequence <= existing.history_coverage_through) ||
    ranges(existing).some(([start, end]) => sequence >= start && sequence <= end)
  );
}

export function mergeTextPublicationCoverage(existing: Coordinates, incoming: Coordinates): Partial<Coordinates> {
  if (!sameIdentity(existing, incoming)) return {};
  const validSequence = (value: number | undefined) => (positiveSequence(value) ? value : 0);
  const through = Math.max(
    validSequence(existing.history_coverage_through),
    validSequence(incoming.history_coverage_through)
  );
  const sequence = Math.max(
    validSequence(existing.source_publication_sequence),
    validSequence(incoming.source_publication_sequence)
  );
  const merged: Array<[number, number]> = [];
  // Keep holes: a higher sequence is not proof that a delayed lower one was seen.
  // Adjacent publications coalesce; an accepted history snapshot retires covered ranges.
  for (const [start, end] of [...ranges(existing), ...ranges(incoming)].toSorted((a, b) => a[0] - b[0])) {
    if (end <= through) continue;
    const nextStart = Math.max(start, through + 1);
    const last = merged.at(-1);
    if (last && nextStart <= last[1] + 1) last[1] = Math.max(last[1], end);
    else merged.push([nextStart, end]);
  }
  return {
    ...(positiveSequence(through) ? { history_coverage_through: through } : {}),
    ...(positiveSequence(sequence) ? { source_publication_sequence: sequence } : {}),
    ...(merged.length || existing.text_publication_ranges ? { text_publication_ranges: merged } : {}),
  };
}
