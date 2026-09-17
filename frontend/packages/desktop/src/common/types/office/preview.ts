/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export type PreviewContentType =
  | 'markdown'
  | 'diff'
  | 'code'
  | 'html'
  | 'pdf'
  | 'ppt'
  | 'word'
  | 'excel'
  | 'image'
  | 'structure'
  | 'molecule'
  | 'table'
  | 'msa'
  | 'genome'
  | 'sequence'
  | 'notebook'
  | 'hdf5'
  | 'latex'
  | 'audio'
  | 'video'
  | 'archive'
  | 'unsupported'
  | 'url';

export interface PreviewHistoryTarget {
  contentType: PreviewContentType;
  file_path?: string;
  workspace?: string;
  file_name?: string;
  title?: string;
  language?: string;
  conversation_id?: string;
  artifact_id?: string;
  version_id?: string;
  content_url?: string;
}

export interface PreviewSnapshotInfo {
  id: string;
  label: string;
  created_at: number;
  size: number;
  contentType: PreviewContentType;
  file_name?: string;
  file_path?: string;
}

export interface RemoteImageFetchRequest {
  url: string;
}
