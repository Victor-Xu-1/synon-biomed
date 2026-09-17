import { useCallback, useEffect, useState } from 'react';
import {
  loadSynonBiomedUserProfile,
  saveSynonBiomedUserProfile,
  subscribeSynonBiomedUserProfile,
  type SynonBiomedUserProfile,
} from '@/renderer/services/synonBiomedUserProfile';

export function useSynonBiomedUserProfile(userId: string | undefined, fallbackName: string) {
  const profileUserId = userId?.trim() || 'local';
  const [profile, setProfile] = useState<SynonBiomedUserProfile>(() =>
    loadSynonBiomedUserProfile(profileUserId, fallbackName)
  );

  useEffect(() => {
    const refresh = () => setProfile(loadSynonBiomedUserProfile(profileUserId, fallbackName));
    refresh();
    return subscribeSynonBiomedUserProfile(profileUserId, refresh);
  }, [fallbackName, profileUserId]);

  const saveProfile = useCallback(
    (next: SynonBiomedUserProfile) => {
      const saved = saveSynonBiomedUserProfile(profileUserId, next);
      setProfile(saved);
      return saved;
    },
    [profileUserId]
  );

  return { profile, saveProfile };
}
