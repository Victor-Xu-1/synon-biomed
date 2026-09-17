import React from 'react';

export function cachePreviewModule<T>(load: () => Promise<T>): () => Promise<T> {
  let pending: Promise<T> | undefined;
  return () => {
    pending ??= load().catch((error) => {
      pending = undefined;
      throw error;
    });
    return pending;
  };
}

export const loadDiffPreview = cachePreviewModule(() => import('./DiffViewer'));
export const loadExcelPreview = cachePreviewModule(() => import('./ExcelViewer'));
export const loadHTMLRenderer = cachePreviewModule(() => import('../renderers/HTMLRenderer'));
export const loadImagePreview = cachePreviewModule(() => import('./ImageViewer'));
export const loadMarkdownPreview = cachePreviewModule(() => import('./MarkdownViewer'));
export const loadPDFPreview = cachePreviewModule(() => import('./PDFViewer'));
export const loadOfficeDocPreview = cachePreviewModule(() => import('./OfficeDocViewer'));
export const loadPptPreview = cachePreviewModule(() => import('./PptViewer'));
export const loadURLPreview = cachePreviewModule(() => import('./URLViewer'));
export const loadSynonBiomedStructureViewer = cachePreviewModule(() => import('./SynonBiomedStructureViewer'));
export const preloadSynonBiomedStructureViewer = (): Promise<void> =>
  loadSynonBiomedStructureViewer().then((): undefined => undefined);
export const loadSynonBiomedTableViewer = cachePreviewModule(() => import('./SynonBiomedTableViewer'));
export const loadSynonBiomedMsaViewer = cachePreviewModule(() => import('./SynonBiomedMsaViewer'));
export const loadSynonBiomedGenomeViewer = cachePreviewModule(() => import('./SynonBiomedGenomeViewer'));
export const loadSynonBiomedSequenceViewer = cachePreviewModule(() => import('./SynonBiomedSequenceViewer'));
export const loadSynonBiomedNotebookViewer = cachePreviewModule(() => import('./SynonBiomedNotebookViewer'));
export const loadSynonBiomedMoleculeViewer = cachePreviewModule(() => import('./SynonBiomedMoleculeViewer'));
export const loadSynonBiomedMcpAppArtifactViewer = cachePreviewModule(
  () => import('./SynonBiomedMcpAppArtifactViewer')
);
export const loadSynonBiomedMcpAppViewer = cachePreviewModule(() => import('./SynonBiomedMcpAppViewer'));
export const loadSynonBiomedJsonViewer = cachePreviewModule(() => import('./SynonBiomedJsonViewer'));
export const loadSynonBiomedHdf5Viewer = cachePreviewModule(() => import('./SynonBiomedHdf5Viewer'));
export const loadMediaPreview = cachePreviewModule(() => import('./MediaPreview'));
export const loadUnsupportedPreview = cachePreviewModule(() => import('./UnsupportedPreview'));
export const loadArchivePreview = cachePreviewModule(() => import('./ArchiveViewer'));

export const LazyDiffPreview = React.lazy(loadDiffPreview);
export const LazyExcelPreview = React.lazy(loadExcelPreview);
export const LazyHTMLRenderer = React.lazy(loadHTMLRenderer);
export const LazyImagePreview = React.lazy(loadImagePreview);
export const LazyMarkdownPreview = React.lazy(loadMarkdownPreview);
export const LazyPDFPreview = React.lazy(loadPDFPreview);
export const LazyOfficeDocPreview = React.lazy(loadOfficeDocPreview);
export const LazyPptPreview = React.lazy(loadPptPreview);
export const LazyURLPreview = React.lazy(loadURLPreview);
export const LazySynonBiomedStructureViewer = React.lazy(loadSynonBiomedStructureViewer);
export const LazySynonBiomedTableViewer = React.lazy(loadSynonBiomedTableViewer);
export const LazySynonBiomedMsaViewer = React.lazy(loadSynonBiomedMsaViewer);
export const LazySynonBiomedGenomeViewer = React.lazy(loadSynonBiomedGenomeViewer);
export const LazySynonBiomedSequenceViewer = React.lazy(loadSynonBiomedSequenceViewer);
export const LazySynonBiomedNotebookViewer = React.lazy(loadSynonBiomedNotebookViewer);
export const LazySynonBiomedMoleculeViewer = React.lazy(loadSynonBiomedMoleculeViewer);
export const LazySynonBiomedMcpAppArtifactViewer = React.lazy(loadSynonBiomedMcpAppArtifactViewer);
export const LazySynonBiomedMcpAppViewer = React.lazy(loadSynonBiomedMcpAppViewer);
export const LazySynonBiomedJsonViewer = React.lazy(loadSynonBiomedJsonViewer);
export const LazySynonBiomedHdf5Viewer = React.lazy(loadSynonBiomedHdf5Viewer);
export const LazyMediaPreview = React.lazy(loadMediaPreview);
export const LazyUnsupportedPreview = React.lazy(loadUnsupportedPreview);
export const LazyArchivePreview = React.lazy(loadArchivePreview);
