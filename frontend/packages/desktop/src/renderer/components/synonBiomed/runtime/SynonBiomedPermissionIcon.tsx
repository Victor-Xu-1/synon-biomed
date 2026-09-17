import React from 'react';
import { iconColors } from '@/renderer/styles/colors';

export type SynonBiomedPermissionIconVariant = 'request' | 'smart' | 'full';

type SynonBiomedPermissionIconProps = {
  variant?: SynonBiomedPermissionIconVariant;
  mode?: string;
  size?: number;
  className?: string;
};

/**
 * Permission glyphs follow the compact Codex visual language:
 * raised hand = always ask, terminal shield = risk-aware approval,
 * orange warning shield = unrestricted access.
 */
const SynonBiomedPermissionIcon: React.FC<SynonBiomedPermissionIconProps> = ({
  variant,
  mode,
  size = 18,
  className = '',
}) => {
  const resolvedVariant = variant ?? permissionIconVariant(mode ?? 'default');
  const color = resolvedVariant === 'full' ? iconColors.warning : 'currentColor';

  return (
    <span
      className={`inline-flex shrink-0 items-center justify-center leading-none ${className}`}
      style={{ width: size, height: size, color }}
      data-permission-icon={resolvedVariant}
      aria-hidden='true'
    >
      <svg
        width={size}
        height={size}
        viewBox='0 0 24 24'
        fill='none'
        stroke='currentColor'
        strokeWidth='1.8'
        strokeLinecap='round'
        strokeLinejoin='round'
      >
        {resolvedVariant === 'request' ? <RequestApprovalGlyph /> : null}
        {resolvedVariant === 'smart' ? <SmartApprovalGlyph /> : null}
        {resolvedVariant === 'full' ? <FullAccessGlyph /> : null}
      </svg>
    </span>
  );
};

const RequestApprovalGlyph: React.FC = () => (
  <>
    <path d='M18 11V6.8a1.7 1.7 0 0 0-3.4 0V11' />
    <path d='M14.6 10.5V4.9a1.7 1.7 0 0 0-3.4 0v5.6' />
    <path d='M11.2 10.8V6.3a1.7 1.7 0 0 0-3.4 0v7.2' />
    <path d='M7.8 13.5v-1.3a1.7 1.7 0 0 0-3.4 0v2.3A8.3 8.3 0 0 0 12.7 23h.1a8.3 8.3 0 0 0 8.3-8.3V9.1a1.55 1.55 0 0 0-3.1 0V11' />
  </>
);

const PermissionShieldOutline: React.FC = () => (
  <path d='M12 2.8 19.2 5.4v5.2c0 4.45-2.72 8.47-7.2 10.7-4.48-2.23-7.2-6.25-7.2-10.7V5.4L12 2.8Z' />
);

const SmartApprovalGlyph: React.FC = () => (
  <>
    <PermissionShieldOutline />
    <path d='m8.7 9.1 2.15 2.05-2.15 2.05' />
    <path d='M12.7 14.2h2.8' />
  </>
);

const FullAccessGlyph: React.FC = () => (
  <>
    <PermissionShieldOutline />
    <path d='M12 7.5v6.2' />
    <circle cx='12' cy='16.7' r='0.8' fill='currentColor' stroke='none' />
  </>
);

export function permissionIconVariant(value: string): SynonBiomedPermissionIconVariant {
  if (isWarningPermissionMode(value)) return 'full';
  if (['smart', 'auto', 'dontAsk', 'autoEdit', 'confirm'].includes(value)) return 'smart';
  return 'request';
}

export function isWarningPermissionMode(value: string): boolean {
  return ['bypassPermissions', 'bypass', 'yolo', 'yoloNoSandbox', 'full-access', 'allow'].includes(value);
}

export default SynonBiomedPermissionIcon;
