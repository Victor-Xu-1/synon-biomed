import { buildToolFailurePresentation } from '@/renderer/pages/conversation/Messages/components/toolFailurePresentation';
import { describe, expect, it } from 'vitest';

describe('Python tool failure presentation', () => {
  it.each([
    ['AttributeError', "AttributeError: 'Uncharger' object has no attribute 'chargeMol'"],
    ['ImportError', "ImportError: cannot import name 'rdStereo' from 'rdkit.Chem'"],
    ['KeyError', "KeyError: 'priority_score'"],
  ])('prefers the final %s over an internal exec stack frame', (_kind, exception) => {
    const raw = [
      'Traceback (most recent call last):',
      '  File "/opt/kernel_worker.py", line 213, in execute',
      '    exec(compiled, namespace)',
      exception,
    ].join('\n');

    const presentation = buildToolFailurePresentation({ stderr: raw });

    expect(presentation.summary).toBe(exception);
    expect(presentation.summary).not.toContain('exec(compiled, namespace)');
    expect(presentation.summary).not.toContain('/opt/kernel_worker.py');
  });

  it('uses a stable code for a truncated structured failure instead of rendering the JSON envelope', () => {
    const presentation = buildToolFailurePresentation(
      '{"cell_index":2,"code":"python_execution_failed","files_written":[{"path":"large-output.dat"}]'
    );

    expect(presentation.errorCode).toBe('python_execution_failed');
    expect(presentation.summary).toBe('python_execution_failed');
    expect(presentation.summary).not.toContain('{"cell_index"');
  });
});
