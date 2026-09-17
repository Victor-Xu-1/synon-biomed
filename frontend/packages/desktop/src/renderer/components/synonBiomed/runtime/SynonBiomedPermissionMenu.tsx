import { Dropdown, Message, Tooltip } from '@arco-design/web-react';
import { Check } from '@icon-park/react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { AcpConfigSetStatus, AcpDerivedOption } from '@/renderer/hooks/synonBiomed/runtime/useAcpConfigOptions';
import SynonBiomedPermissionIcon, { isWarningPermissionMode } from './SynonBiomedPermissionIcon';

type SynonBiomedPermissionMenuProps = {
  option: AcpDerivedOption | null;
  setStatus: AcpConfigSetStatus;
  setConfigOption: (optionId: string, value: string) => Promise<unknown>;
  /** New conversations use the local assistant override contract. Existing
   * conversations must render only modes advertised by the live runtime. */
  includeStandardModes?: boolean;
  disabled?: boolean;
};

const STANDARD_PERMISSION_MODES: Array<{ value: string; label: string; description: string | null }> = [
  { value: 'default', label: 'Default', description: null },
  { value: 'smart', label: 'Smart', description: null },
  { value: 'bypassPermissions', label: 'Bypass Permissions', description: null },
] as const;

const permissionRowClass =
  'composer-control-menu__row session-options-menu__row box-border flex h-32px w-full cursor-pointer items-center gap-8px border-0 bg-transparent px-12px text-left font-inherit text-14px leading-none text-t-primary outline-none hover:bg-[var(--color-fill-2)] focus-visible:bg-[var(--color-fill-2)] disabled:cursor-default disabled:opacity-50';

const SynonBiomedPermissionMenu: React.FC<SynonBiomedPermissionMenuProps> = ({
  option,
  setStatus,
  setConfigOption,
  includeStandardModes = false,
  disabled = false,
}) => {
  const { i18n, t } = useTranslation();
  const [popupVisible, setPopupVisible] = useState(false);
  const currentValue = option?.currentValue ?? 'default';
  const [selectedValue, setSelectedValue] = useState(currentValue);
  const isSetting = setStatus.state === 'setting';
  const options = useMemo(() => {
    const merged = [...(includeStandardModes ? STANDARD_PERMISSION_MODES : []), ...(option?.options ?? [])];
    const seen = new Set<string>();
    return merged
      .filter((item) => {
        if (seen.has(item.value)) return false;
        seen.add(item.value);
        return true;
      })
      .map((item) => ({
        value: item.value,
        label: permissionModeLabel(item.value, item.label, i18n, t),
        description: permissionModeDescription(item.value, item.description, t),
      }));
  }, [i18n, includeStandardModes, option?.options, t]);

  useEffect(() => {
    setSelectedValue(currentValue);
  }, [currentValue]);

  const optionId = option?.id ?? 'mode';
  if (options.length === 0) return null;

  const selectedOption = options.find((item) => item.value === selectedValue) ?? options[0];
  const isUnavailable = disabled || isSetting;
  const selectPermission = async (value: string) => {
    setPopupVisible(false);
    if (value === selectedValue || isUnavailable) return;
    try {
      await setConfigOption(optionId, value);
      setSelectedValue(value);
      Message.success(t('agentMode.switchSuccess'));
    } catch (error) {
      console.error('[SynonBiomedPermissionMenu] Failed to set permission mode', error);
      Message.error(t('agentMode.switchFailed'));
    }
  };

  const menu = (
    <div
      className='app-overlay-menu composer-control-menu box-border overflow-y-auto rd-8px py-6px'
      data-testid='synon-biomed-permission-menu'
      role='menu'
      aria-label={t('agentMode.switchMode')}
      style={{
        width: 'min(210px, calc(100vw - 24px))',
        maxWidth: 'calc(100vw - 24px)',
        maxHeight: 'calc(100vh - 24px)',
        opacity: 1,
      }}
    >
      {options.map((item) => {
        const selected = item.value === selectedValue;
        const warning = isWarningPermissionMode(item.value);
        return (
          <Tooltip key={item.value} content={item.description} position='left'>
            <button
              type='button'
              role='menuitemradio'
              aria-checked={selected}
              aria-label={`${item.label}: ${item.description}`}
              title={item.description}
              data-testid={`synon-biomed-permission-option-${item.value}`}
              className={permissionRowClass}
              disabled={isUnavailable}
              onClick={() => void selectPermission(item.value)}
            >
              <span className='inline-flex w-20px shrink-0 text-t-tertiary'>
                <SynonBiomedPermissionIcon mode={item.value} size={18} />
              </span>
              <span className='min-w-0 flex-1 truncate' style={warning ? { color: 'var(--warning)' } : undefined}>
                {item.label}
              </span>
              <span
                className='inline-flex w-20px shrink-0 justify-end text-t-primary'
                style={warning ? { color: 'var(--warning)' } : undefined}
              >
                {selected ? <Check theme='outline' size={15} fill={warning ? 'var(--warning)' : undefined} /> : null}
              </span>
            </button>
          </Tooltip>
        );
      })}
    </div>
  );

  return (
    <Dropdown
      trigger='click'
      position='tl'
      popupVisible={popupVisible}
      onVisibleChange={(visible) => !isUnavailable && setPopupVisible(visible)}
      droplist={menu}
    >
      <button
        type='button'
        data-testid='synon-biomed-permission-selector'
        data-current-permission={selectedOption.value}
        aria-label={`${t('agentMode.permission')} · ${selectedOption.label}`}
        title={`${selectedOption.label}: ${selectedOption.description}`}
        disabled={isUnavailable}
        onClick={() => setPopupVisible((visible) => !visible)}
        className='composer-icon-control relative'
      >
        <SynonBiomedPermissionIcon mode={selectedOption.value} size={17} />
      </button>
    </Dropdown>
  );
};

function isFullAccessMode(value: string): boolean {
  return isWarningPermissionMode(value);
}

function permissionModeLabel(
  value: string,
  fallback: string,
  i18n: ReturnType<typeof useTranslation>['i18n'],
  t: ReturnType<typeof useTranslation>['t']
): string {
  if (value === 'default') return t('agentMode.requestApproval');
  if (value === 'smart') return t('agentMode.helpApprove');
  if (value === 'bypassPermissions') return t('agentMode.fullAccess');
  return i18n.exists(`agentMode.${value}`) ? t(`agentMode.${value}`) : fallback;
}

function permissionModeDescription(
  value: string,
  provided: string | null | undefined,
  t: ReturnType<typeof useTranslation>['t']
): string {
  if (['default', 'smart', 'bypassPermissions'].includes(value)) return fallbackPermissionDescription(value, t);
  return provided || fallbackPermissionDescription(value, t);
}

function fallbackPermissionDescription(value: string, t: ReturnType<typeof useTranslation>['t']): string {
  if (isFullAccessMode(value)) {
    return t('agentMode.fullAccessDescription');
  }
  if (['smart', 'auto', 'dontAsk', 'autoEdit'].includes(value)) {
    return t('agentMode.riskyOperationsDescription');
  }
  return t('agentMode.approvalRequiredDescription');
}

export default SynonBiomedPermissionMenu;
