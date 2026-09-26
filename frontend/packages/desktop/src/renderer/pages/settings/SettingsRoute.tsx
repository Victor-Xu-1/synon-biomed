import React, { Suspense } from 'react';
import { Navigate, useParams } from 'react-router';
import AppLoader from '@renderer/components/layout/AppLoader';
import { isSettingsRouteId, settingsRouteLoaders, type SettingsRouteId } from './settingsRouteLoaders';
import './components/settings.css';

const settingsPages: Record<SettingsRouteId, React.LazyExoticComponent<React.ComponentType>> = {
  account: React.lazy(settingsRouteLoaders.account),
  'plans-usage': React.lazy(settingsRouteLoaders['plans-usage']),
  experts: React.lazy(settingsRouteLoaders.experts),
  skills: React.lazy(settingsRouteLoaders.skills),
  tools: React.lazy(settingsRouteLoaders.tools),
  models: React.lazy(settingsRouteLoaders.models),
  compute: React.lazy(settingsRouteLoaders.compute),
  governance: React.lazy(settingsRouteLoaders.governance),
  network: React.lazy(settingsRouteLoaders.network),
  environments: React.lazy(settingsRouteLoaders.environments),
  credentials: React.lazy(settingsRouteLoaders.credentials),
  storage: React.lazy(settingsRouteLoaders.storage),
  general: React.lazy(settingsRouteLoaders.general),
};

const SettingsRoute: React.FC = () => {
  const { section } = useParams<{ section: string }>();
  if (!isSettingsRouteId(section)) return <Navigate to='/settings/experts' replace />;

  const Page = settingsPages[section];
  return (
    <Suspense key={section} fallback={<AppLoader />}>
      <Page />
    </Suspense>
  );
};

export default SettingsRoute;
