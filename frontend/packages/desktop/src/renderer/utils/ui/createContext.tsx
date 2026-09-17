/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { FunctionComponent, PropsWithChildren } from 'react';
import React, { useEffect, useRef, useState } from 'react';

type FN<P> = FunctionComponent<PropsWithChildren<P>>;

type F2D<T> = T | ((data: T) => T);

export const createContext = <T,>(defaultValue: T): [() => T, FN<{ value: T }>, () => (value: F2D<T>) => void] => {
  const Context = React.createContext<{
    value: T;
    setValue: (value: T) => void;
  }>({
    value: defaultValue,
    setValue() {
      console.warn('');
    },
  });

  const useContext = () => {
    return React.useContext(Context).value;
  };

  const useUpdateContext = () => {
    return React.useContext(Context).setValue;
  };

  const DefaultValue = defaultValue;
  const ContextComponent: FN<{ value: T }> = (props) => {
    const [contextValue, setValue] = useState(props.value || JSON.parse(JSON.stringify(DefaultValue)));
    const isFirst = useRef(true);
    useEffect(() => {
      if (isFirst.current) return;
      setValue(props.value);
      isFirst.current = false;
    }, [props.value]);
    return <Context.Provider value={{ value: contextValue, setValue }}>{props.children}</Context.Provider>;
  };

  return [useContext, ContextComponent, useUpdateContext];
};

export default createContext;
