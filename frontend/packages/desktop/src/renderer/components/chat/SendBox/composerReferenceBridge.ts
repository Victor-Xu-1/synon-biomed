export type ComposerArtifactReferenceInput = {
  filename: string;
  artifactId: string;
  versionId?: string;
  conversationId?: string;
};

type ComposerReferenceSink = (reference: ComposerArtifactReferenceInput) => void;

const sinks = new Map<string, ComposerReferenceSink>();
let activeConversationId: string | null = null;

export function registerComposerReferenceSink(conversationId: string, sink: ComposerReferenceSink): () => void {
  sinks.set(conversationId, sink);
  activeConversationId = conversationId;
  return () => {
    if (sinks.get(conversationId) === sink) sinks.delete(conversationId);
    if (activeConversationId === conversationId) activeConversationId = null;
  };
}

export function markComposerReferenceSinkActive(conversationId: string): void {
  if (sinks.has(conversationId)) activeConversationId = conversationId;
}

export function insertArtifactReferenceIntoActiveComposer(input: ComposerArtifactReferenceInput): boolean {
  const conversationId = input.conversationId || activeConversationId;
  if (!conversationId || !input.artifactId.trim() || !input.versionId?.trim() || !input.filename.trim()) return false;
  const sink = sinks.get(conversationId);
  if (!sink) return false;

  sink(input);
  return true;
}
