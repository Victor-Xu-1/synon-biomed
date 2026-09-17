import {
  loadSynonBiomedComputeProviders,
  loadSynonBiomedSessionComputeProviders,
  setSynonBiomedSessionComputeProvider,
  type SynonBiomedComputeProvider,
} from '@/renderer/services/synonBiomedCompute';
import { resolveLocaleKey } from '@/common/utils';
import { loadSynonBiomedAssistants } from '@/renderer/services/synonBiomedCatalog';
import type { SynonBiomedSessionOptions } from '@/renderer/services/synonBiomedSessionOptions';
import { resolveAssistantName } from '@/renderer/utils/model/assistantDisplay';
import { Dropdown, Message, Spin, Switch, Tooltip } from '@arco-design/web-react';
import { Check, Right, SettingConfig } from '@icon-park/react';
import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';

type SpecialistOption = { id: string; label: string; agentId: string };
type Submenu = 'specialist' | 'compute' | null;

export type SynonBiomedSessionOptionsMenuProps = {
  rootFrameId?: string;
  value: SynonBiomedSessionOptions;
  onChange: (next: SynonBiomedSessionOptions) => void;
  onPersistentOptionChange?: (key: 'autoReview' | 'memory', value: boolean) => void | Promise<void>;
  computeSelection?: string[];
  onComputeSelectionChange?: (providers: string[]) => void;
  onClearGoal?: () => void | Promise<void>;
  goalClearInFlight?: boolean;
  disabled?: boolean;
};

const rowClass =
  'composer-control-menu__row session-options-menu__row box-border flex h-32px w-full cursor-pointer items-center border-0 bg-transparent px-12px text-left font-inherit text-14px leading-none text-t-primary outline-none hover:bg-[var(--color-fill-2)] focus-visible:bg-[var(--color-fill-2)] disabled:cursor-default disabled:opacity-50';
const checkedSwitchStyle: React.CSSProperties = { backgroundColor: '#2b7fdc' };
const clampToRange = (candidate: number, minimum: number, maximum: number) =>
  Math.min(Math.max(candidate, minimum), maximum);

const SynonBiomedSessionOptionsMenu: React.FC<SynonBiomedSessionOptionsMenuProps> = ({
  rootFrameId,
  value,
  onChange,
  onPersistentOptionChange,
  computeSelection,
  onComputeSelectionChange,
  onClearGoal,
  goalClearInFlight = false,
  disabled = false,
}) => {
  const { t, i18n } = useTranslation();
  const localeKey = resolveLocaleKey(i18n.language);
  const navigate = useNavigate();
  const translationRef = useRef(t);
  translationRef.current = t;
  const [visible, setVisible] = useState(false);
  const [submenu, setSubmenu] = useState<Submenu>(null);
  const [loading, setLoading] = useState(false);
  const [pendingProvider, setPendingProvider] = useState<string | null>(null);
  const [pendingOption, setPendingOption] = useState<'autoReview' | 'memory' | null>(null);
  const [specialists, setSpecialists] = useState<SpecialistOption[]>([]);
  const [computeProviders, setComputeProviders] = useState<SynonBiomedComputeProvider[]>([]);
  const [enabledComputeProviders, setEnabledComputeProviders] = useState<string[]>(
    (computeSelection ?? []).filter((provider) => provider !== 'local')
  );
  const menuSurfaceRef = useRef<HTMLDivElement>(null);
  const submenuRef = useRef<HTMLDivElement>(null);
  const submenuCloseTimerRef = useRef<number | null>(null);
  const [submenuPosition, setSubmenuPosition] = useState<React.CSSProperties>();

  const cancelSubmenuClose = () => {
    if (submenuCloseTimerRef.current === null) return;
    window.clearTimeout(submenuCloseTimerRef.current);
    submenuCloseTimerRef.current = null;
  };

  const scheduleSubmenuClose = () => {
    cancelSubmenuClose();
    submenuCloseTimerRef.current = window.setTimeout(() => {
      setSubmenu(null);
      submenuCloseTimerRef.current = null;
    }, 220);
  };

  useEffect(
    () => () => {
      if (submenuCloseTimerRef.current !== null) window.clearTimeout(submenuCloseTimerRef.current);
    },
    []
  );

  useEffect(() => {
    if (!visible) {
      setSubmenu(null);
      return;
    }
    let active = true;
    setLoading(true);
    void Promise.all([
      loadSynonBiomedAssistants(),
      loadSynonBiomedComputeProviders(),
      rootFrameId ? loadSynonBiomedSessionComputeProviders(rootFrameId) : Promise.resolve(computeSelection ?? []),
    ])
      .then(([assistants, providers, enabledProviders]) => {
        if (!active) return;
        setSpecialists(
          assistants
            .map((assistant) => ({
              id: assistant.id,
              label: resolveAssistantName(assistant, localeKey, assistant.name),
              agentId: assistant.agent_id || assistant.name,
            }))
            .filter((assistant) => assistant.agentId && assistant.agentId !== 'OPERON')
        );
        setComputeProviders(providers.filter((provider) => provider.checked && provider.name !== 'local'));
        setEnabledComputeProviders(enabledProviders.filter((provider) => provider !== 'local'));
      })
      .catch((error) => {
        console.error('Failed to load session options:', error);
        if (active) Message.error(translationRef.current('conversation.synonRuntime.sessionOptions.loadFailed'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [computeSelection, localeKey, rootFrameId, visible]);

  useEffect(() => {
    if (computeSelection) setEnabledComputeProviders(computeSelection.filter((provider) => provider !== 'local'));
  }, [computeSelection]);

  useLayoutEffect(() => {
    if (!submenu) return;

    const updateSubmenuPosition = () => {
      const menuRect = menuSurfaceRef.current?.getBoundingClientRect();
      const submenuRect = submenuRef.current?.getBoundingClientRect();
      if (!menuRect || !submenuRect) return;

      const viewportWidth = window.innerWidth;
      const viewportHeight = window.innerHeight;
      const viewportInset = 12;
      const gap = 6;
      const canOpenRight = menuRect.right + gap + submenuRect.width <= viewportWidth - viewportInset;
      const canOpenLeft = menuRect.left - gap - submenuRect.width >= viewportInset;
      const preferredLeft = canOpenRight || !canOpenLeft ? menuRect.width + gap : -submenuRect.width - gap;
      const left = clampToRange(
        preferredLeft,
        viewportInset - menuRect.left,
        viewportWidth - viewportInset - submenuRect.width - menuRect.left
      );
      const top = clampToRange(
        menuRect.height - submenuRect.height,
        viewportInset - menuRect.top,
        viewportHeight - viewportInset - submenuRect.height - menuRect.top
      );
      setSubmenuPosition({ left, top });
    };

    updateSubmenuPosition();
    window.addEventListener('resize', updateSubmenuPosition);
    const resizeObserver =
      typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(updateSubmenuPosition);
    if (menuSurfaceRef.current) resizeObserver?.observe(menuSurfaceRef.current);
    if (submenuRef.current) resizeObserver?.observe(submenuRef.current);

    return () => {
      window.removeEventListener('resize', updateSubmenuPosition);
      resizeObserver?.disconnect();
    };
  }, [submenu]);

  const activeSpecialistLabel =
    value.targetAgent === 'OPERON'
      ? t('conversation.synonRuntime.sessionOptions.none')
      : specialists.find((specialist) => specialist.agentId === value.targetAgent)?.label || value.targetAgent;

  const computeLabel = useMemo(() => {
    if (enabledComputeProviders.length === 0) return t('conversation.synonRuntime.sessionOptions.localMachine');
    if (enabledComputeProviders.length === 1) {
      const selected = computeProviders.find((provider) => provider.name === enabledComputeProviders[0]);
      return selected?.displayName || enabledComputeProviders[0];
    }
    return t('conversation.synonRuntime.sessionOptions.providerCount', { count: enabledComputeProviders.length });
  }, [computeProviders, enabledComputeProviders, t]);

  const updateComputeProvider = async (providerName: string, checked: boolean) => {
    setPendingProvider(providerName);
    try {
      if (rootFrameId) await setSynonBiomedSessionComputeProvider(rootFrameId, providerName, checked);
      setEnabledComputeProviders((current) => {
        const next = checked
          ? Array.from(new Set([...current, providerName]))
          : current.filter((name) => name !== providerName);
        onComputeSelectionChange?.(next);
        return next;
      });
    } catch (error) {
      console.error('Failed to update session compute provider:', error);
      Message.error(t('conversation.synonRuntime.sessionOptions.computeUpdateFailed'));
    } finally {
      setPendingProvider(null);
    }
  };

  const selectLocalCompute = async () => {
    if (enabledComputeProviders.length === 0 || pendingProvider !== null) return;
    setPendingProvider('__local__');
    try {
      if (rootFrameId) {
        await Promise.all(
          enabledComputeProviders.map((providerName) =>
            setSynonBiomedSessionComputeProvider(rootFrameId, providerName, false)
          )
        );
      }
      setEnabledComputeProviders([]);
      onComputeSelectionChange?.([]);
    } catch (error) {
      console.error('Failed to restore local session compute:', error);
      Message.error(t('conversation.synonRuntime.sessionOptions.computeUpdateFailed'));
    } finally {
      setPendingProvider(null);
    }
  };

  const updateToggle = async (key: 'delegation' | 'autoReview' | 'memory', checked: boolean) => {
    if (key === 'delegation') {
      onChange({ ...value, delegation: checked });
      return;
    }

    setPendingOption(key);
    try {
      await onPersistentOptionChange?.(key, checked);
      if (!onPersistentOptionChange) onChange({ ...value, [key]: checked });
    } catch (error) {
      console.error('Failed to update session option:', error);
      Message.error(t('conversation.synonRuntime.sessionOptions.updateFailed'));
    } finally {
      setPendingOption(null);
    }
  };

  const toggleRows: Array<{
    key: 'delegation' | 'autoReview' | 'memory';
    label: string;
    description: string;
    checked: boolean;
  }> = [
    {
      key: 'delegation',
      label: t('conversation.synonRuntime.sessionOptions.delegation'),
      description: t('conversation.synonRuntime.sessionOptions.delegationDescription'),
      checked: value.delegation,
    },
    {
      key: 'autoReview',
      label: t('conversation.synonRuntime.sessionOptions.autoReview'),
      description: t('conversation.synonRuntime.sessionOptions.autoReviewDescription'),
      checked: value.autoReview,
    },
    {
      key: 'memory',
      label: t('conversation.synonRuntime.sessionOptions.memory'),
      description: t('conversation.synonRuntime.sessionOptions.memoryDescription'),
      checked: value.memory,
    },
  ];

  const submenuPanel = submenu ? (
    <div
      ref={submenuRef}
      className='app-overlay-menu composer-control-submenu absolute box-border overflow-y-auto rd-8px py-6px'
      role='menu'
      aria-label={
        submenu === 'specialist'
          ? t('conversation.synonRuntime.sessionOptions.expert')
          : t('conversation.synonRuntime.sessionOptions.compute')
      }
      style={{
        width: 'min(300px, calc(100vw - 24px))',
        maxWidth: 'calc(100vw - 24px)',
        maxHeight: 'calc(100vh - 24px)',
        opacity: 1,
        ...submenuPosition,
      }}
      onMouseEnter={cancelSubmenuClose}
      onMouseLeave={scheduleSubmenuClose}
    >
      {submenu === 'specialist' ? (
        <>
          <button
            type='button'
            role='menuitemradio'
            aria-checked={value.targetAgent === 'OPERON'}
            aria-label={t('conversation.synonRuntime.sessionOptions.selectExpert', {
              name: t('conversation.synonRuntime.sessionOptions.none'),
            })}
            className={rowClass}
            onClick={() => onChange({ ...value, targetAgent: 'OPERON' })}
          >
            <span className='inline-flex w-20px'>{value.targetAgent === 'OPERON' ? <Check size={14} /> : null}</span>
            <span>{t('conversation.synonRuntime.sessionOptions.none')}</span>
          </button>
          {specialists.map((specialist) => (
            <button
              key={specialist.id}
              type='button'
              role='menuitemradio'
              aria-checked={specialist.agentId === value.targetAgent}
              aria-label={t('conversation.synonRuntime.sessionOptions.selectExpert', { name: specialist.label })}
              className={rowClass}
              onClick={() => onChange({ ...value, targetAgent: specialist.agentId })}
            >
              <span className='inline-flex w-20px'>
                {specialist.agentId === value.targetAgent ? <Check size={14} /> : null}
              </span>
              <span className='truncate'>{specialist.label}</span>
            </button>
          ))}
          {!loading && specialists.length === 0 ? (
            <div className='px-12px py-8px text-12px text-t-tertiary'>
              {t('conversation.synonRuntime.sessionOptions.noExperts')}
            </div>
          ) : null}
          <div role='separator' className='mx-12px my-5px h-1px bg-fill-3' />
          <button
            type='button'
            role='menuitem'
            className={rowClass}
            onClick={() => {
              setVisible(false);
              setSubmenu(null);
              void navigate('/settings/experts?create=1');
            }}
          >
            {t('conversation.synonRuntime.sessionOptions.createExpert')}
          </button>
        </>
      ) : (
        <>
          <button
            type='button'
            role='menuitemradio'
            aria-checked={enabledComputeProviders.length === 0}
            className={`${rowClass} gap-10px`}
            disabled={pendingProvider !== null}
            onClick={() => void selectLocalCompute()}
          >
            <span className='inline-flex w-20px'>
              {enabledComputeProviders.length === 0 ? <Check size={14} /> : null}
            </span>
            <span>{t('conversation.synonRuntime.sessionOptions.localMachine')}</span>
          </button>
          {computeProviders.map((provider) => {
            const checked = enabledComputeProviders.includes(provider.name);
            return (
              <div key={provider.name} role='none' className={`${rowClass} cursor-default gap-10px`}>
                <span className='min-w-0 flex-1 truncate'>{provider.displayName}</span>
                <Switch
                  size='small'
                  role='menuitemcheckbox'
                  aria-checked={checked}
                  aria-label={t('conversation.synonRuntime.sessionOptions.computeNamed', {
                    name: provider.displayName,
                  })}
                  checked={checked}
                  style={checked ? checkedSwitchStyle : undefined}
                  loading={pendingProvider === provider.name}
                  disabled={pendingProvider !== null && pendingProvider !== provider.name}
                  onChange={(next) => void updateComputeProvider(provider.name, next)}
                />
              </div>
            );
          })}
          {!loading && computeProviders.length === 0 ? (
            <div className='px-12px py-8px text-12px leading-18px text-t-tertiary'>
              {t('conversation.synonRuntime.sessionOptions.noCompute')}
            </div>
          ) : null}
          <div role='separator' className='mx-12px my-5px h-1px bg-fill-3' />
          <button
            type='button'
            role='menuitem'
            className={rowClass}
            onClick={() => {
              setVisible(false);
              setSubmenu(null);
              void navigate('/settings/compute');
            }}
          >
            {t('conversation.synonRuntime.sessionOptions.manageCompute')}
          </button>
        </>
      )}
    </div>
  ) : null;

  const dropdownContent = (
    <div
      ref={menuSurfaceRef}
      className='app-overlay-menu composer-control-menu relative box-border overflow-visible rd-8px py-6px'
      role='menu'
      aria-label={t('conversation.synonRuntime.sessionOptions.title')}
      onClick={(event) => event.stopPropagation()}
      onMouseEnter={cancelSubmenuClose}
      onMouseLeave={scheduleSubmenuClose}
      style={{
        width: 'min(210px, calc(100vw - 24px))',
        maxWidth: 'calc(100vw - 24px)',
        maxHeight: 'calc(100vh - 24px)',
        opacity: 1,
      }}
    >
      {toggleRows.map((option) => {
        const optionPending = option.key !== 'delegation' && pendingOption === option.key;
        return (
          <Tooltip key={option.key} content={option.description} position='left'>
            <button
              type='button'
              role='menuitemcheckbox'
              aria-checked={option.checked}
              data-testid={`session-config-row-${option.key}`}
              className={`${rowClass} justify-between`}
              disabled={pendingOption !== null}
              onClick={() => void updateToggle(option.key, !option.checked)}
            >
              <span>{option.label}</span>
              <span
                aria-hidden='true'
                className={`relative inline-flex h-20px w-36px shrink-0 items-center rounded-full transition-colors ${
                  option.checked ? 'bg-[#2b7fdc]' : 'bg-fill-4'
                } ${pendingOption !== null ? 'opacity-60' : ''}`}
              >
                {optionPending ? (
                  <span className='absolute inset-0 flex items-center justify-center'>
                    <Spin size={12} />
                  </span>
                ) : (
                  <span
                    className='h-16px w-16px rounded-full bg-white transition-transform'
                    style={{ transform: `translateX(${option.checked ? 18 : 2}px)` }}
                  />
                )}
              </span>
            </button>
          </Tooltip>
        );
      })}
      {value.goalText ? (
        <div className='px-12px py-6px'>
          <div className='truncate text-12px text-t-secondary' title={value.goalText}>
            {t('conversation.synonRuntime.sessionOptions.goal', { goal: value.goalText })}
          </div>
          {onClearGoal ? (
            <button
              type='button'
              role='menuitem'
              className='mt-4px border-0 bg-transparent p-0 text-12px text-primary disabled:opacity-50'
              disabled={goalClearInFlight}
              onClick={() => void onClearGoal()}
            >
              {goalClearInFlight
                ? t('conversation.synonRuntime.sessionOptions.clearingGoal')
                : t('conversation.synonRuntime.sessionOptions.clearGoal')}
            </button>
          ) : null}
        </div>
      ) : null}
      {value.asRoutine ? (
        <div className='px-12px py-6px text-12px text-t-tertiary'>
          {t('conversation.synonRuntime.sessionOptions.routineUnsupported')}
        </div>
      ) : null}
      <div role='separator' className='mx-12px my-5px h-1px bg-fill-3' />
      <button
        type='button'
        role='menuitem'
        aria-haspopup='menu'
        aria-expanded={submenu === 'specialist'}
        data-testid='session-config-row-specialist'
        className={`${rowClass} justify-between`}
        onMouseEnter={() => {
          cancelSubmenuClose();
          setSubmenu('specialist');
        }}
        onMouseLeave={() => {
          if (submenu === 'specialist') scheduleSubmenuClose();
        }}
        onFocus={() => setSubmenu('specialist')}
        onClick={() => {
          cancelSubmenuClose();
          setSubmenu('specialist');
        }}
      >
        <span>{t('conversation.synonRuntime.sessionOptions.expert')}</span>
        <span className='flex min-w-0 items-center gap-8px text-t-tertiary'>
          <span className='max-w-88px truncate'>{activeSpecialistLabel}</span>
          <Right size={14} />
        </span>
      </button>
      <button
        type='button'
        role='menuitem'
        aria-haspopup='menu'
        aria-expanded={submenu === 'compute'}
        data-testid='session-config-row-compute'
        className={`${rowClass} justify-between`}
        onMouseEnter={() => {
          cancelSubmenuClose();
          setSubmenu('compute');
        }}
        onMouseLeave={() => {
          if (submenu === 'compute') scheduleSubmenuClose();
        }}
        onFocus={() => setSubmenu('compute')}
        onClick={() => {
          cancelSubmenuClose();
          setSubmenu('compute');
        }}
      >
        <span>{t('conversation.synonRuntime.sessionOptions.compute')}</span>
        <span className='flex min-w-0 items-center gap-8px text-t-tertiary'>
          <span className='max-w-88px truncate'>{computeLabel}</span>
          <Right size={14} />
        </span>
      </button>
      {loading ? (
        <div
          className='absolute inset-x-0 bottom-0 flex h-24px items-center justify-center'
          aria-label={t('conversation.synonRuntime.sessionOptions.loading')}
        >
          <Spin size={14} />
        </div>
      ) : null}
      {submenuPanel}
    </div>
  );

  return (
    <Dropdown
      trigger='click'
      position='tl'
      popupVisible={visible}
      onVisibleChange={(next) => !disabled && setVisible(next)}
      droplist={dropdownContent}
    >
      <button
        type='button'
        aria-label={t('conversation.synonRuntime.sessionOptions.title')}
        data-testid='synon-biomed-session-options-trigger'
        disabled={disabled}
        className='composer-icon-control relative'
      >
        <SettingConfig theme='outline' size={17} strokeWidth={3.6} />
        {value.delegation ||
        value.autoReview ||
        value.memory ||
        value.targetAgent !== 'OPERON' ||
        enabledComputeProviders.length > 0 ? (
          <span className='absolute right-5px top-4px h-5px w-5px rounded-full bg-primary' aria-hidden='true' />
        ) : null}
      </button>
    </Dropdown>
  );
};

export default SynonBiomedSessionOptionsMenu;
