/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import LightweightOfficeViewer from './LightweightOfficeViewer';

interface PptViewerProps {
  file_path?: string;
  artifactId?: string;
  versionId?: string;
  content?: string;
  workspace?: string;
}

const PptViewer: React.FC<PptViewerProps> = (props) => <LightweightOfficeViewer docType='ppt' {...props} />;

export default PptViewer;
