import {
  createSynonBiomedSessionDefaults,
  type SynonBiomedSessionOptions,
} from '@/renderer/services/synonBiomedSessionOptions';
import { useEffect, useState } from 'react';

type GuidSessionOptionsInput = {
  baseAgentName: string;
  navigationKey: string;
  resetRequested: boolean;
};

/**
 * Owns the lifecycle of an unsent conversation draft. New-task navigation
 * resets every optional module to the product default, while changing the base
 * assistant only updates the draft's base agent.
 */
export function useGuidSessionOptions({ baseAgentName, navigationKey, resetRequested }: GuidSessionOptionsInput) {
  const [sessionOptions, setSessionOptions] = useState<SynonBiomedSessionOptions>(() =>
    createSynonBiomedSessionDefaults(baseAgentName)
  );
  const [sessionComputeProviders, setSessionComputeProviders] = useState<string[]>([]);

  useEffect(() => {
    if (!resetRequested) return;
    setSessionOptions(createSynonBiomedSessionDefaults(baseAgentName));
    setSessionComputeProviders([]);
  }, [baseAgentName, navigationKey, resetRequested]);

  useEffect(() => {
    setSessionOptions((current) =>
      current.targetAgent === baseAgentName ? current : { ...current, targetAgent: baseAgentName }
    );
  }, [baseAgentName]);

  return {
    sessionOptions,
    setSessionOptions,
    sessionComputeProviders,
    setSessionComputeProviders,
  };
}
