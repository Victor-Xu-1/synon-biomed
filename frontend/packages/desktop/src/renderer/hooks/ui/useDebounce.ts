import type React from 'react';
import { useCallback, useEffect, useRef } from 'react';

/**
 * 防抖 Hook
 * @param callback 需要防抖的函数
 * @param delay 防抖延迟时间（毫秒）
 * @returns 防抖后的函数
 */
function useDebounce<Args extends unknown[]>(
  callback: (...args: Args) => void,
  delay: number,
  deps: React.DependencyList
): (...args: Args) => void {
  const timeoutRef = useRef<NodeJS.Timeout | null>(null);

  // 清理定时器
  const clearTimer = useCallback(() => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current);
      timeoutRef.current = null;
    }
  }, []);

  // 组件卸载时清理
  useEffect(() => {
    return () => {
      clearTimer();
    };
  }, [clearTimer]);

  const debouncedFunction = useCallback(
    (...args: Args) => {
      clearTimer();
      timeoutRef.current = setTimeout(() => {
        callback(...args);
      }, delay);
    },
    [delay, clearTimer, ...deps]
  );

  return debouncedFunction;
}

export default useDebounce;
