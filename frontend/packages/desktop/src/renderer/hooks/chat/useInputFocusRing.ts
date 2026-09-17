export const useInputFocusRing = () => {
  return {
    activeBorderColor: 'var(--composer-border-active, #d6d6d6)',
    inactiveBorderColor: 'var(--composer-border, #e5e5e5)',
    activeShadow: 'var(--composer-shadow-active, 0 0 0 1px rgba(23, 23, 23, 0.06), 0 12px 34px rgba(23, 23, 23, 0.1))',
    inactiveShadow: 'var(--composer-shadow, 0 8px 28px rgba(23, 23, 23, 0.07))',
    surfaceBackgroundColor: 'var(--composer-surface, #fff)',
  };
};
