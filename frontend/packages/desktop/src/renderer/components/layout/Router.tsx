import React, { Suspense } from 'react';
import { HashRouter, Navigate, Outlet, Route, Routes } from 'react-router';
import { useTranslation } from 'react-i18next';
import { Refresh } from '@icon-park/react';
import AppLoader from '@renderer/components/layout/AppLoader';
import { useAuth } from '@renderer/hooks/context/AuthContext';
import { consumeAuthResumeDestination } from '@renderer/services/authSession';
import {
  clearOnboardingCompletionOwnerSession,
  useOnboardingCompletionGate,
} from '@renderer/services/onboardingCompletionAuthority';
import { loadConversationRoute } from '@renderer/pages/conversation/conversationRoute';
import {
  loadArtifactPreviewRoute,
  loadAuthenticatedWorkspaceRoute,
  loadGuidRoute,
  loadProjectRoute,
  loadSettingsRoute,
} from './routeModules';
import './AuthUnavailablePanel.css';

const AuthenticatedWorkspace = React.lazy(loadAuthenticatedWorkspaceRoute);
const Conversation = React.lazy(loadConversationRoute);
const Guid = React.lazy(loadGuidRoute);
const Onboarding = React.lazy(() => import('@renderer/pages/onboarding'));
const ProjectRoute = React.lazy(loadProjectRoute);
const ArtifactPreview = React.lazy(loadArtifactPreviewRoute);
const LoginPage = React.lazy(() => import('@renderer/pages/login'));
const SettingsRoute = React.lazy(loadSettingsRoute);

const withRouteFallback = (Component: React.LazyExoticComponent<React.ComponentType>) => (
  <Suspense fallback={<AppLoader />}>
    <Component />
  </Suspense>
);

const AuthUnavailablePanel: React.FC = () => {
  const { t } = useTranslation();
  const { failure, refresh } = useAuth();
  const messageKey = failure === 'server' ? 'login.errors.serverError' : 'login.errors.networkError';
  return (
    <section className='auth-unavailable' aria-labelledby='auth-unavailable-title' data-testid='auth-unavailable-panel'>
      <div className='auth-unavailable__card'>
        <div className='auth-unavailable__brand' aria-label={t('login.brand')}>
          <span className='auth-unavailable__brand-mark' aria-hidden='true'>
            <img src='./pwa/icon-192.png?v=9b986028' alt='' />
          </span>
          <span>{t('login.brand')}</span>
        </div>

        <div className='auth-unavailable__status-mark' aria-hidden='true'>
          <span className='auth-unavailable__status-dot' />
        </div>

        <div className='auth-unavailable__copy'>
          <p className='auth-unavailable__eyebrow'>{t('login.unavailable.eyebrow')}</p>
          <h1 id='auth-unavailable-title'>{t('login.unavailable.title')}</h1>
          <p role='alert' className='auth-unavailable__message'>
            {t(messageKey)}
          </p>
          <p className='auth-unavailable__hint'>{t('login.unavailable.hint')}</p>
        </div>

        <button type='button' className='auth-unavailable__retry' onClick={() => void refresh()}>
          <Refresh theme='outline' size={15} />
          <span>{t('login.unavailable.retry')}</span>
        </button>
      </div>
    </section>
  );
};

const ProtectedLayout: React.FC = () => {
  const { status } = useAuth();
  if (status === 'checking') return <AppLoader />;
  if (status === 'unavailable') return <AuthUnavailablePanel />;
  if (status !== 'authenticated') return <Navigate to='/login' replace />;
  return withRouteFallback(AuthenticatedWorkspace);
};

const ProtectedPage: React.FC<React.PropsWithChildren> = ({ children }) => {
  const { status } = useAuth();
  if (status === 'checking') return <AppLoader />;
  if (status === 'unavailable') return <AuthUnavailablePanel />;
  if (status !== 'authenticated') return <Navigate to='/login' replace />;
  return <>{children}</>;
};

const AuthenticatedRedirect: React.FC = () => {
  const [destination] = React.useState(consumeAuthResumeDestination);
  return <Navigate to={destination} replace state={{ resetAssistant: true, selectLatestProject: true }} />;
};

const LoginRoute: React.FC = () => {
  const { status } = useAuth();
  if (status === 'checking') return <AppLoader />;
  if (status === 'unavailable') return <AuthUnavailablePanel />;
  if (status === 'authenticated') return <AuthenticatedRedirect />;
  return withRouteFallback(LoginPage);
};

const UnknownRoute: React.FC = () => {
  const { status } = useAuth();
  if (status === 'checking') return <AppLoader />;
  if (status === 'unavailable') return <AuthUnavailablePanel />;
  return <Navigate to={status === 'authenticated' ? '/guid' : '/login'} replace />;
};

export const FirstUseGate: React.FC = () => {
  const { t } = useTranslation();
  const { user } = useAuth();
  const ownerId = user?.id?.trim() ?? '';
  const { state, retry } = useOnboardingCompletionGate(ownerId);

  if (state === 'checking') return <AppLoader />;
  if (state === 'required') return <Navigate to='/onboarding' replace />;
  if (state === 'error') {
    return (
      <section
        className='flex h-full min-h-240px items-center justify-center p-24px'
        aria-labelledby='first-use-error-title'
      >
        <div className='flex max-w-440px flex-col items-center gap-12px text-center'>
          <h1 id='first-use-error-title' className='text-18px font-600 text-t-primary'>
            {t('guid.onboarding.error.loadTitle')}
          </h1>
          <p role='alert' className='text-13px leading-20px text-danger-7'>
            {t('guid.onboarding.error.loadDescription')}
          </p>
          <button
            type='button'
            className='rounded-8px bg-primary-6 px-16px py-8px text-13px font-500 text-white outline-none hover:bg-primary-5 focus-visible:ring-2 focus-visible:ring-primary-4 focus-visible:ring-offset-2'
            onClick={() => void retry()}
          >
            {t('common.retry')}
          </button>
        </div>
      </section>
    );
  }
  return <Outlet />;
};

const OnboardingOwnerSessionLifecycle: React.FC = () => {
  const { status, user } = useAuth();
  const previousOwnerId = React.useRef<string | null>(null);

  React.useEffect(() => {
    const ownerId = status === 'authenticated' ? (user?.id?.trim() ?? '') : '';
    if (ownerId) {
      if (previousOwnerId.current && previousOwnerId.current !== ownerId) {
        clearOnboardingCompletionOwnerSession(previousOwnerId.current);
      }
      previousOwnerId.current = ownerId;
      return;
    }
    if (status === 'unauthenticated' && previousOwnerId.current) {
      clearOnboardingCompletionOwnerSession(previousOwnerId.current);
      previousOwnerId.current = null;
    }
  }, [status, user?.id]);

  return null;
};

const PanelRoute: React.FC = () => {
  return (
    <HashRouter>
      <OnboardingOwnerSessionLifecycle />
      <Routes>
        <Route path='/login' element={<LoginRoute />} />
        <Route path='/onboarding' element={<ProtectedPage>{withRouteFallback(Onboarding)}</ProtectedPage>} />
        <Route element={<ProtectedLayout />}>
          <Route element={<FirstUseGate />}>
            <Route index element={<Navigate to='/guid' replace />} />
            <Route path='/guid' element={withRouteFallback(Guid)} />
            <Route path='/projects/:projectId' element={withRouteFallback(ProjectRoute)} />
            <Route path='/projects/:projectId/workbench' element={<Navigate to='..' relative='path' replace />} />
            <Route path='/artifacts/:artifactId' element={withRouteFallback(ArtifactPreview)} />
            <Route path='/conversation/:id' element={withRouteFallback(Conversation)} />
            <Route path='/settings/skills/import-history' element={<Navigate to='/settings/skills' replace />} />
            <Route path='/compute' element={<Navigate to='/settings/compute' replace />} />
            <Route path='/settings/appearance' element={<Navigate to='/settings/general' replace />} />
            <Route path='/settings/pet' element={<Navigate to='/settings/general' replace />} />
            <Route path='/settings/system' element={<Navigate to='/settings/general' replace />} />
            <Route path='/settings/about' element={<Navigate to='/settings/general' replace />} />
            <Route path='/settings/:section' element={withRouteFallback(SettingsRoute)} />
            <Route path='/settings' element={<Navigate to='/settings/experts' replace />} />
            <Route path='*' element={<Navigate to='/guid' replace />} />
          </Route>
        </Route>
        <Route path='*' element={<UnknownRoute />} />
      </Routes>
    </HashRouter>
  );
};

export default PanelRoute;
