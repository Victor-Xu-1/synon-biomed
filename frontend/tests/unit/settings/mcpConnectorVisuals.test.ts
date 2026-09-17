import { describe, expect, it } from 'vitest';
import {
  KNOWN_MCP_CONNECTOR_VISUALS,
  resolveMcpConnectorVisual,
} from '@/renderer/pages/settings/ToolsSettings/mcpConnectorVisuals';

describe('MCP connector visuals', () => {
  it('assigns every bundled connector a distinct first-party glyph', () => {
    const entries = Object.entries(KNOWN_MCP_CONNECTOR_VISUALS);
    expect(entries).toHaveLength(34);
    expect(new Set(entries.map(([, visual]) => visual.glyph)).size).toBe(34);
    for (const [name, visual] of entries) {
      expect(resolveMcpConnectorVisual(`bundled:${name}`)).toEqual(visual);
    }
  });

  it('uses a stable visual for unknown marketplace and custom connectors', () => {
    const first = resolveMcpConnectorVisual('org.example/new-protein-service', 'New Protein Service');
    const second = resolveMcpConnectorVisual('org.example/new-protein-service', 'New Protein Service');
    expect(first).toEqual(second);
    expect(first.glyph).toMatch(/^custom-/);
  });
});
