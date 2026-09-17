import React from 'react';

export const SYNON_BIOMED_AVATAR_SRC = '/branding/synon-biomed-avatar.png';

export const isRobotAvatar = (value?: string | null): boolean => value?.trim() === '🤖';

type SynonBiomedAvatarProps = {
  size?: number | string;
  className?: string;
  alt?: string;
};

/** Shared non-robot identity image for Synon Biomed agents and runtime UI. */
const SynonBiomedAvatar: React.FC<SynonBiomedAvatarProps> = ({ size = 16, className = '', alt = '' }) => (
  <img
    src={SYNON_BIOMED_AVATAR_SRC}
    alt={alt}
    aria-hidden={alt ? undefined : true}
    className={`block object-contain ${className}`.trim()}
    style={{ width: size, height: size }}
  />
);

export default SynonBiomedAvatar;
