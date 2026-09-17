/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import LightweightOfficeViewer from './LightweightOfficeViewer';

interface ExcelPreviewProps {
  file_path?: string;
  artifactId?: string;
  versionId?: string;
  content?: string;
  workspace?: string;
}

const ExcelPreview: React.FC<ExcelPreviewProps> = (props) => <LightweightOfficeViewer docType='excel' {...props} />;

export default ExcelPreview;
