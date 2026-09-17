export type SynonBiomedMcpAppSaveContract = {
  appTool: string;
  resultField: string;
  mimeType: string;
  extension: string;
  filenameStem: string;
  hasChangeField?: string;
  emptyTemplate?: string;
};

export const ketcherMcpAppSaveContract: SynonBiomedMcpAppSaveContract = Object.freeze({
  appTool: 'get_structure',
  resultField: 'ket',
  mimeType: 'application/json',
  extension: '.ket',
  filenameStem: 'sketcher',
  hasChangeField: 'has_change',
  emptyTemplate: '{"root":{"nodes":[]}}',
});
