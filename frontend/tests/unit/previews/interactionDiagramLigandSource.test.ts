import { describe, expect, it } from 'vitest';
import {
  findCandidateSmiles,
  loadCurrentInteractionDiagramCandidateSmiles,
  loadInteractionDiagramCandidateSmiles,
  loadInteractionDiagramCandidateSmilesFromSources,
  resolveCurrentInteractionDiagramCompanionUrl,
} from '@/renderer/pages/conversation/Preview/components/viewers/interactionDiagramLigandSource';

describe('interactionDiagramLigandSource', () => {
  it('resolves either common SMILES-first or candidate-first records by exact candidate identity', () => {
    expect(findCandidateSmiles('SMILES candidate_id\nCC(=O)O TYK2-001\nN#CC1=CC=CC=C1 TYK2-026', 'TYK2-026')).toBe(
      'N#CC1=CC=CC=C1'
    );
    expect(findCandidateSmiles('candidate_id,smiles\nTYK2-026,CN1CCC(C#N)CC1', 'TYK2-026')).toBe('CN1CCC(C#N)CC1');
    expect(findCandidateSmiles('CCO TYK2-0260', 'TYK2-026')).toBeNull();
  });

  it('rejects non-SMILES payloads on the companion-file boundary', () => {
    expect(findCandidateSmiles('TYK2-026,<script>alert(1)</script>', 'TYK2-026')).toBeNull();
  });

  it('loads the exact candidate through the existing same-deliverable companion URL', async () => {
    const fetchImpl = async () => new Response('SMILES candidate_id\nCCO TYK2-026', { status: 200 });
    await expect(
      loadInteractionDiagramCandidateSmiles('/api/artifacts/smi/versions/v1', 'TYK2-026', fetchImpl)
    ).resolves.toBe('CCO');
  });

  it('recovers from an older incomplete companion by consulting the current artifact source', async () => {
    const requested: string[] = [];
    const fetchImpl = async (input: RequestInfo | URL) => {
      requested.push(String(input));
      return new Response(
        String(input).endsWith('/old')
          ? 'candidate_id\tcanonical_smiles\n'
          : 'candidate_id\tcanonical_smiles\nTYK2-026\tCN1CCC(C#N)CC1\n',
        { status: 200 }
      );
    };

    await expect(
      loadInteractionDiagramCandidateSmilesFromSources(
        ['/api/artifacts/smi/versions/old', '/api/artifacts/smi/versions/current'],
        'TYK2-026',
        fetchImpl
      )
    ).resolves.toBe('CN1CCC(C#N)CC1');
    expect(requested).toEqual(['/api/artifacts/smi/versions/old', '/api/artifacts/smi/versions/current']);
  });

  it('resolves the latest immutable version of the same artifact before parsing', async () => {
    const requested: string[] = [];
    const fetchImpl = async (input: RequestInfo | URL) => {
      requested.push(String(input));
      if (String(input).endsWith('/versions')) {
        return new Response(
          JSON.stringify([
            { version_id: 'version-old', version_number: 1 },
            { version_id: 'version-current', version_number: 2 },
          ]),
          { status: 200, headers: { 'content-type': 'application/json' } }
        );
      }
      return new Response('candidate_id\tcanonical_smiles\nTYK2-026\tCN1CCC(C#N)CC1\n', { status: 200 });
    };

    await expect(
      resolveCurrentInteractionDiagramCompanionUrl('/api/artifacts/artifact-1/versions/version-old', fetchImpl)
    ).resolves.toBe('/api/artifacts/versions/version-current');
    await expect(
      loadCurrentInteractionDiagramCandidateSmiles(
        '/api/artifacts/artifact-1/versions/version-old',
        'TYK2-026',
        fetchImpl
      )
    ).resolves.toBe('CN1CCC(C#N)CC1');
    expect(requested).toContain('/api/artifacts/artifact-1/versions');
    expect(requested).toContain('/api/artifacts/versions/version-current');
  });

  it('rejects external and oversized companion sources before RDKit processing', async () => {
    const fetchImpl = async () => new Response('CCO TYK2-026', { status: 200 });
    await expect(
      loadInteractionDiagramCandidateSmiles('https://example.org/candidates.smi', 'TYK2-026', fetchImpl)
    ).rejects.toThrow('INTERACTION_SMILES_URL_INVALID');
    await expect(
      loadInteractionDiagramCandidateSmiles(
        '/api/artifacts/smi/versions/v1',
        'TYK2-026',
        async () => new Response('x', { headers: { 'content-length': String(1024 * 1024 + 1) } })
      )
    ).rejects.toThrow('INTERACTION_SMILES_FILE_TOO_LARGE');
  });
});
