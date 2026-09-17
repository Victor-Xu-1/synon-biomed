import { describe, expect, it } from 'vitest';
import { portableStaticCopySource } from '../../../vite.config';

describe('Vite static-copy source paths', () => {
  it('normalizes Windows paths before they enter the glob boundary', () => {
    expect(portableStaticCopySource(String.raw`C:\synon-biomed\frontend\LICENSE`)).toBe(
      'C:/synon-biomed/frontend/LICENSE'
    );
  });

  it('preserves already portable paths', () => {
    expect(portableStaticCopySource('/opt/synon-biomed/frontend/LICENSE')).toBe('/opt/synon-biomed/frontend/LICENSE');
  });
});
