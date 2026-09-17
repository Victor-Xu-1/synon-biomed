/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * SynonAI 基础组件库统一导出 / SynonAI base components unified exports
 *
 * 提供所有基础组件和类型的统一导出入口
 * Provides unified export entry for all base components and types
 */

// ==================== 组件导出 / Component Exports ====================

export { default as SynonModal } from './SynonModal';
export { default as SynonCollapse } from './SynonCollapse';
export { default as SynonSelect } from './SynonSelect';
export { default as SynonScrollArea } from './SynonScrollArea';
export { default as SynonSteps } from './SynonSteps';

// ==================== 类型导出 / Type Exports ====================

// SynonModal 类型 / SynonModal types
export type {
  ModalSize,
  ModalHeaderConfig,
  ModalFooterConfig,
  ModalContentStyleConfig,
  SynonModalProps,
} from './SynonModal';
export { MODAL_SIZES } from './SynonModal';

// SynonCollapse 类型 / SynonCollapse types
export type { SynonCollapseProps, SynonCollapseItemProps } from './SynonCollapse';

// SynonSelect 类型 / SynonSelect types
export type { SynonSelectProps } from './SynonSelect';

// SynonSteps 类型 / SynonSteps types
export type { SynonStepsProps } from './SynonSteps';
