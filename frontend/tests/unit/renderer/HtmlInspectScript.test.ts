import { describe, expect, it } from 'vitest';
import {
  generateHtmlAnnotationHighlightScript,
  generateInspectScript,
} from '@/renderer/pages/conversation/Preview/components/renderers/htmlInspectScript';

describe('HTML annotation inspect script', () => {
  it('emits an executable script with stable-selector and visible-text contracts', () => {
    const script = generateInspectScript(true, { copySuccess: 'selected' });
    expect(() => new Function(script)).not.toThrow();
    expect(script).toContain('getStableSelector');
    expect(script).toContain('__SYNON_AI_INSPECT_ELEMENT__');
    expect(script).toContain('elementText');
    expect(script).toContain('viewportWidth');
  });

  it('serializes existing annotations into an executable selector highlight script', () => {
    const script = generateHtmlAnnotationHighlightScript([
      { id: 'annotation-1', label: '①', text: 'Review row', selector: '#results > tr:nth-of-type(2)' },
    ]);
    expect(() => new Function(script)).not.toThrow();
    expect(script).toContain('data-synon-ai-html-annotation');
    expect(script).toContain('__SYNON_AI_HTML_ANNOTATION_CLICK__');
    expect(script).toContain('#results > tr:nth-of-type(2)');
  });
});
