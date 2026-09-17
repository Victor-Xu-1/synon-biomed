import { loadSynonBiomedProjectBenches } from '@/renderer/services/synonBiomedGateway';
import { Button, Result, Spin } from '@arco-design/web-react';
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router';
import { resolveProjectNavigationTarget } from './projectNavigationModel';

const ProjectRoute: React.FC = () => {
  const { projectId } = useParams();
  const navigate = useNavigate();
  const { t } = useTranslation();
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId) {
      void navigate('/guid', { replace: true });
      return;
    }

    let active = true;
    setError(null);
    void loadSynonBiomedProjectBenches(projectId)
      .then((benches) => {
        if (!active) return;
        const target = resolveProjectNavigationTarget(projectId, benches);
        void navigate(target.pathname, target.state ? { replace: true, state: target.state } : { replace: true });
      })
      .catch((reason: unknown) => {
        console.error('[ProjectRoute] Failed to open project:', reason);
        if (active) setError(t('conversation.history.projectOpenFailedDescription'));
      });

    return () => {
      active = false;
    };
  }, [attempt, navigate, projectId, t]);

  if (error) {
    return (
      <div className='size-full flex items-center justify-center px-20px' data-testid='project-route-error'>
        <Result
          status='error'
          title={t('conversation.history.projectOpenFailed')}
          subTitle={error}
          extra={
            <Button type='primary' onClick={() => setAttempt((current) => current + 1)}>
              {t('common.retry')}
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div className='flex items-center justify-center pt-40px' data-testid='project-route-loading' role='status'>
      <Spin size={20} />
    </div>
  );
};

export default ProjectRoute;
