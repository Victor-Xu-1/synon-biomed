/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import LightweightOfficeViewer from './LightweightOfficeViewer';

interface OfficeDocPreviewProps {
  file_path?: string;
  artifactId?: string;
  versionId?: string;
  content?: string;
  workspace?: string;
}

const OfficeDocPreview: React.FC<OfficeDocPreviewProps> = (props) => (
  <LightweightOfficeViewer docType='word' {...props} />
);

export default OfficeDocPreview;
