/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { ComponentType, PropsWithChildren } from 'react';
import React from 'react';

type WrapperComponent = ComponentType<PropsWithChildren>;

function HOC<WrapperProps extends object>(WrapperComponent: ComponentType<PropsWithChildren<WrapperProps>>) {
  return <Props extends WrapperProps>(Component: ComponentType<Props>): React.FC<Props> => {
    return (props: Props) => (
      <WrapperComponent {...props}>
        <Component {...props} />
      </WrapperComponent>
    );
  };
}

// 从右到左，对原组件进行HOC操作
const Wrapper = (...wrapperComponents: WrapperComponent[]) => {
  return <Props extends object>(Component: ComponentType<Props>): React.FC<Props> => {
    return wrapperComponents.toReversed().reduce<React.FC<Props>>(
      (WrappedComponent, WrapperComponent) => {
        return (props: Props) => (
          <WrapperComponent>
            <WrappedComponent {...props} />
          </WrapperComponent>
        );
      },
      (props: Props) => <Component {...props} />
    );
  };
};

HOC.Wrapper = Wrapper;

export default HOC;
