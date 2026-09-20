import type { TFunction } from 'i18next';

export function scientificRuntimePresentation(id: string, t: TFunction): { title: string; description: string } {
  switch (id) {
    case 'common-structure-toolkit':
      return {
        title: t('guid.onboarding.capabilities.runtimeCommonTitle'),
        description: t('guid.onboarding.capabilities.runtimeCommonDescription'),
      };
    case 'structure-interaction':
      return {
        title: t('guid.onboarding.capabilities.runtimeInteractionTitle'),
        description: t('guid.onboarding.capabilities.runtimeInteractionDescription'),
      };
    case 'biomolecular-electrostatics':
      return {
        title: t('guid.onboarding.capabilities.runtimeElectrostaticsTitle'),
        description: t('guid.onboarding.capabilities.runtimeElectrostaticsDescription'),
      };
    case 'autodock-vina':
      return {
        title: t('guid.onboarding.capabilities.runtimeVinaTitle'),
        description: t('guid.onboarding.capabilities.runtimeVinaDescription'),
      };
    case 'drug-chemistry-process':
      return {
        title: t('guid.onboarding.capabilities.runtimeDrugChemistryTitle'),
        description: t('guid.onboarding.capabilities.runtimeDrugChemistryDescription'),
      };
    case 'molecular-conversion-quantum':
      return {
        title: t('guid.onboarding.capabilities.runtimeConversionTitle'),
        description: t('guid.onboarding.capabilities.runtimeConversionDescription'),
      };
    case 'qsar-admet-classical':
      return {
        title: t('guid.onboarding.capabilities.runtimeQSARTitle'),
        description: t('guid.onboarding.capabilities.runtimeQSARDescription'),
      };
    case 'clinical-pharmacometrics':
      return {
        title: t('guid.onboarding.capabilities.runtimeClinicalTitle'),
        description: t('guid.onboarding.capabilities.runtimeClinicalDescription'),
      };
    case 'single-cell-omics':
      return {
        title: t('guid.onboarding.capabilities.runtimeOmicsTitle'),
        description: t('guid.onboarding.capabilities.runtimeOmicsDescription'),
      };
    case 'genomics-command-line':
      return {
        title: t('guid.onboarding.capabilities.runtimeGenomicsTitle'),
        description: t('guid.onboarding.capabilities.runtimeGenomicsDescription'),
      };
    case 'molecular-simulation':
      return {
        title: t('guid.onboarding.capabilities.runtimeSimulationTitle'),
        description: t('guid.onboarding.capabilities.runtimeSimulationDescription'),
      };
    case 'medical-imaging':
      return {
        title: t('guid.onboarding.capabilities.runtimeImagingTitle'),
        description: t('guid.onboarding.capabilities.runtimeImagingDescription'),
      };
    case 'instrument-data-analytics':
      return {
        title: t('guid.onboarding.capabilities.runtimeInstrumentTitle'),
        description: t('guid.onboarding.capabilities.runtimeInstrumentDescription'),
      };
    default:
      return { title: id, description: t('guid.onboarding.capabilities.runtimeUnknown') };
  }
}
