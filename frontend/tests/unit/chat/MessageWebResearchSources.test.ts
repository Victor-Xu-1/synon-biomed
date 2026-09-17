import { describe, expect, it } from 'vitest';
import {
  extractResearchSourcePresentation,
  isResearchActivityTool,
} from '@/renderer/pages/conversation/Messages/components/researchSourcePresentation';

describe('MessageWebResearchSources model', () => {
  it.each(['web_search', 'WebSearch', 'WebResearch', 'web_research'])('recognizes %s as a research tool', (name) => {
    expect(isResearchActivityTool(name)).toBe(true);
  });

  it.each(['web_fetch', 'fetch_article_fulltext', 'download_public_scientific_file'])(
    'keeps %s in the retrieval-detail projection instead of a search-result card',
    (name) => {
      expect(isResearchActivityTool(name)).toBe(false);
    }
  );

  it('extracts only safe HTTP sources, removes duplicates and respects the limit', () => {
    const output = [
      'https://example.org/a, https://example.org/a',
      'https://pubmed.ncbi.nlm.nih.gov/17187687/',
      'javascript:alert(1)',
      'file:///etc/passwd',
      'https://rcsb.org/structure/8OIZ',
    ].join('\n');

    expect(extractResearchSourcePresentation(undefined, output, 2)).toEqual({
      query: null,
      results: [
        {
          key: 'https://example.org/a',
          title: 'example.org/a',
          url: 'https://example.org/a',
          source: 'https://example.org/a',
          snippet: null,
        },
        {
          key: 'https://pubmed.ncbi.nlm.nih.gov/17187687/',
          title: 'pubmed.ncbi.nlm.nih.gov/17187687',
          url: 'https://pubmed.ncbi.nlm.nih.gov/17187687/',
          source: 'https://pubmed.ncbi.nlm.nih.gov/17187687/',
          snippet: null,
        },
      ],
    });
  });

  it('does not render loopback, private-network, credential-bearing, or local-host research links', () => {
    const presentation = extractResearchSourcePresentation(
      undefined,
      [
        'http://127.0.0.1/private',
        'http://192.168.1.10/source',
        'https://user:password@example.org/private',
        'https://example.org/signed?access_token=secret-value',
        'https://example.org/signed?X-Amz-Signature=abcdef',
        'http://[::ffff:127.0.0.1]/private',
        'http://evidence.local/study',
        'https://example.org/public',
      ].join('\n')
    );

    expect(presentation.results).toEqual([expect.objectContaining({ url: 'https://example.org/public' })]);
  });

  it('does not reinterpret structured numeric counters as PDB identifiers', () => {
    const presentation = extractResearchSourcePresentation(
      undefined,
      JSON.stringify({
        api_total: 4821,
        records: [{ id: 'one' }, { id: 'two' }],
      })
    );

    expect(presentation.results).toHaveLength(2);
    expect(presentation.results.map((result) => result.title)).toEqual(['one', 'two']);
    expect(presentation.results.some((result) => result.url?.includes('/structure/4821'))).toBe(false);
  });

  it('does not inflate structured source collections from serialized diagnostic URLs', () => {
    const presentation = extractResearchSourcePresentation(
      undefined,
      JSON.stringify({
        sources: [
          { title: 'Primary study', url: 'https://example.org/study' },
          {
            title: 'Independent study',
            url: 'https://example.org/independent',
          },
        ],
        diagnostics: {
          providerEndpoint: 'https://search.example.org/query',
          debugReferences: ['https://example.org/not-a-returned-source'],
        },
      })
    );

    expect(presentation.results.map((result) => result.url)).toEqual([
      'https://example.org/study',
      'https://example.org/independent',
    ]);
  });

  it('prefers canonical sources over raw provider candidates', () => {
    const presentation = extractResearchSourcePresentation(
      undefined,
      JSON.stringify({
        result: {
          results: [
            {
              title: 'Raw provider hit',
              url: 'https://provider.example.org/raw',
            },
            {
              title: 'Another raw hit',
              url: 'https://provider.example.org/other',
            },
          ],
          sources: [
            {
              title: 'Ranked source',
              url: 'https://evidence.example.org/ranked',
            },
          ],
        },
      })
    );

    expect(presentation.results.map((result) => result.url)).toEqual(['https://evidence.example.org/ranked']);
  });

  it('renders scientific database records instead of treating nested identifiers as separate search hits', () => {
    const presentation = extractResearchSourcePresentation(
      JSON.stringify({ gene_symbol: 'TYK2' }),
      JSON.stringify({
        count: 2,
        targets: [
          {
            target_chembl_id: 'CHEMBL3553',
            pref_name: 'Non-receptor tyrosine-protein kinase TYK2',
            components: [{ target_component_xrefs: [{ xref_id: '8TB6' }, { xref_id: '8S9A' }] }],
          },
          { target_chembl_id: 'CHEMBL2363062', pref_name: 'Janus kinase family' },
        ],
      })
    );

    expect(presentation.results).toEqual([
      {
        key: 'CHEMBL3553:Non-receptor tyrosine-protein kinase TYK2',
        title: 'Non-receptor tyrosine-protein kinase TYK2',
        url: null,
        source: 'CHEMBL3553',
        snippet: null,
      },
      {
        key: 'CHEMBL2363062:Janus kinase family',
        title: 'Janus kinase family',
        url: null,
        source: 'CHEMBL2363062',
        snippet: null,
      },
    ]);
  });

  it('preserves every clinical-trial identity when several records share the same title', () => {
    const trials = Array.from({ length: 20 }, (_, index) => ({
      nct_id: `NCT${String(index + 1).padStart(8, '0')}`,
      title: 'A shared study title that must not become the record identity',
      condition: 'Solid tumor',
    }));
    const presentation = extractResearchSourcePresentation(
      JSON.stringify({ condition: 'Solid tumor' }),
      JSON.stringify({ trials })
    );

    expect(presentation.query).toBe('Solid tumor');
    expect(presentation.results).toHaveLength(20);
    expect(new Set(presentation.results.map((result) => result.key))).toHaveLength(20);
    expect(presentation.results[0]).toMatchObject({
      source: 'NCT00000001',
      url: null,
    });
  });

  it('links only explicitly typed publication identifiers instead of guessing from a generic numeric id', () => {
    const presentation = extractResearchSourcePresentation(
      JSON.stringify({ query: 'kinase evidence' }),
      JSON.stringify({
        records: [
          { id: '123456', title: 'Generic database record' },
          { pmid: '41010003', title: 'Typed PubMed record' },
        ],
      })
    );

    expect(presentation.results).toHaveLength(2);
    expect(presentation.results[0]).toMatchObject({ source: '123456', url: null });
    expect(presentation.results[1]).toMatchObject({
      source: '41010003',
      url: 'https://pubmed.ncbi.nlm.nih.gov/41010003/',
    });
  });
});
