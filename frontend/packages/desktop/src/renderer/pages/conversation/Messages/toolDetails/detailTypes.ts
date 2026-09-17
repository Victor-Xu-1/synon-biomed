import type { ToolPublicDetailKind } from '../toolActivityPresentationRegistry';
import type { ToolPublicDetailBlock } from '../components/toolPublicDetailBlocks';
import type { SynonBiomedPlanDocument } from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';

export type ToolPublicDetailRow = {
  label: string;
  value: string;
};

export type ToolPublicDetailCollection = {
  label: string;
  items: string[];
  total: number;
};

export type ToolPublicPlanAssessment = {
  confidence: string;
  rationale: string | null;
};

export type ToolDetailValueNode =
  | {
      kind: 'scalar';
      path: string;
      label: string;
      value: string;
    }
  | {
      kind: 'object';
      path: string;
      label: string;
      children: ToolDetailValueNode[];
      total: number;
    }
  | {
      kind: 'array';
      path: string;
      label: string;
      children: ToolDetailValueNode[];
      total: number;
    };

export type ToolPublicDetailPresentation = {
  detailKind: ToolPublicDetailKind;
  toolLabel: string;
  toolContext: string | null;
  resultSummary: string | null;
  planSummary: string | null;
  inputRows: ToolPublicDetailRow[];
  inputCollections: ToolPublicDetailCollection[];
  resultRows: ToolPublicDetailRow[];
  resultCollections: ToolPublicDetailCollection[];
  inputBlocks: ToolPublicDetailBlock[];
  outputBlocks: ToolPublicDetailBlock[];
  inputTree: ToolDetailValueNode | null;
  outputTree: ToolDetailValueNode | null;
  narrative: string[];
  notices: string[];
  planDocument: SynonBiomedPlanDocument | null;
  planAssessment: ToolPublicPlanAssessment | null;
  showIdentity: boolean;
  showGenericOutput: boolean;
  inputBlockMode: 'inline' | 'disclosure';
  collectionsInitiallyExpanded: boolean;
};
