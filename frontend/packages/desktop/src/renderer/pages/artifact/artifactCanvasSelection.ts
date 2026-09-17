import type { SynonBiomedArtifactTextSelection } from './artifactTextSelection';

export interface SynonBiomedArtifactPointSelection {
  type: 'point';
  text: string;
  x: number;
  y: number;
  xPercent: number;
  yPercent: number;
  pageNumber: number | null;
}

export interface SynonBiomedArtifactHtmlElementSelection {
  type: 'html_element';
  text: string;
  x: number;
  y: number;
  xPercent: number;
  yPercent: number;
  elementSelector: string;
  elementDescriptor: string;
}

export type SynonBiomedArtifactCanvasSelection =
  | SynonBiomedArtifactTextSelection
  | SynonBiomedArtifactPointSelection
  | SynonBiomedArtifactHtmlElementSelection;

export function clampPercent(value: number): number {
  return Math.min(100, Math.max(0, Number(value.toFixed(4))));
}
