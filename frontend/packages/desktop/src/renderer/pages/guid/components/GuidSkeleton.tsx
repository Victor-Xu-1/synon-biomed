/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import styles from '../index.module.css';

/** Skeleton placeholder for the AssistantSelectionArea while experts load. */
export const AssistantsSkeleton: React.FC = () => {
  const widths = [80, 100, 90];
  return (
    <div className='mt-16px w-full'>
      <div className='flex flex-wrap gap-8px justify-center'>
        {widths.map((w, i) => (
          <div key={i} className={styles.skeletonPill} style={{ width: w, height: 28 }} />
        ))}
      </div>
    </div>
  );
};
