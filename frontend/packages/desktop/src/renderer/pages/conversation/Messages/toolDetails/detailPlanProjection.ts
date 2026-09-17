import {
  normalizeSynonBiomedPlanDocument,
  type SynonBiomedPlanDocument,
  type SynonBiomedPlanStep,
} from '@/renderer/components/synonBiomed/runtime/runtimeOperationsModel';
import { sanitizePublicTaskText } from '../components/toolPublicDetailBlocks';
import { isRecord } from './detailFormatting';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';

export function buildToolPlanDocument(
  input: Record<string, unknown>,
  chinese: boolean
): SynonBiomedPlanDocument | null {
  const nestedPlan = isRecord(input.plan) ? input.plan : null;
  const base = nestedPlan
    ? {
        ...nestedPlan,
        task_summary: nestedPlan.task_summary ?? input.task_summary,
        feasibility: nestedPlan.feasibility ?? input.feasibility,
      }
    : input;
  const candidate =
    Array.isArray(base.phases) || !Array.isArray(base.steps)
      ? base
      : {
          ...base,
          phases: [
            {
              id: 'plan-phase-1',
              name: toolPublicDetailText(chinese, 'planSteps'),
              steps: base.steps,
              delegations: [],
            },
          ],
        };
  let document: SynonBiomedPlanDocument;
  try {
    document = normalizeSynonBiomedPlanDocument(candidate);
  } catch {
    return null;
  }

  const phases = document.phases.flatMap((phase) => {
    const name = sanitizePublicTaskText(phase.name);
    if (!name) return [];
    const steps = phase.steps.flatMap(sanitizeStep);
    const delegations = phase.delegations.flatMap((delegation) => {
      const delegationName = sanitizePublicTaskText(delegation.name);
      if (!delegationName) return [];
      return [
        {
          ...delegation,
          name: delegationName,
          agentName: delegation.agentName ? sanitizePublicTaskText(delegation.agentName) : null,
          steps: delegation.steps.flatMap(sanitizeStep),
        },
      ];
    });
    return [{ ...phase, name, steps, delegations }];
  });
  if (phases.length === 0) return null;
  const taskSummary = sanitizePublicTaskText(document.taskSummary) ?? toolPublicDetailText(chinese, 'planTitle');
  const rationale = document.feasibility?.rationale ? sanitizePublicTaskText(document.feasibility.rationale) : null;
  return {
    ...document,
    taskSummary,
    phases,
    feasibility: document.feasibility
      ? {
          confidence: document.feasibility.confidence,
          rationale,
        }
      : null,
  };
}

function sanitizeStep(step: SynonBiomedPlanStep): SynonBiomedPlanStep[] {
  const title = sanitizePublicTaskText(step.title);
  if (!title) return [];
  const description = sanitizePublicTaskText(step.description) ?? '';
  return [{ ...step, title, description }];
}
