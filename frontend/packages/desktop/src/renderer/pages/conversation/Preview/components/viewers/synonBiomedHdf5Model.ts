export type Hdf5NodeKind = 'group' | 'dataset' | 'datatype' | 'link';

export type Hdf5AttributeSummary = {
  name: string;
  value: string;
};

export type Hdf5TreeNode = {
  name: string;
  path: string;
  kind: Hdf5NodeKind;
  shape?: number[] | null;
  dtype?: string;
  attributes: Hdf5AttributeSummary[];
  children?: Hdf5TreeNode[];
};

export type Hdf5DatasetDetail = {
  path: string;
  shape: number[] | null;
  dtype: string;
  preview: unknown;
  truncated: boolean;
};

export type AnnDataDistributionItem = {
  label: string;
  labelKey?: 'unlabeled' | 'other';
  count: number;
};

export type AnnDataDistribution = {
  key: string;
  label: string;
  labelKey?: 'cellType' | 'responder' | 'response' | 'recist' | 'pathology' | 'sex';
  total: number;
  items: AnnDataDistributionItem[];
};

export type AnnDataEmbedding = {
  key: string;
  label: string;
  labelKey?: 'pcaPreview';
  source: 'stored' | 'derived';
  totalPoints: number;
  sampledPoints: number;
  points: [number, number][];
};

export type AnnDataOverview = {
  cells: number;
  features: number;
  encodingVersion?: string;
  distributions: AnnDataDistribution[];
  embedding?: AnnDataEmbedding;
};

export type Hdf5WorkerRequest =
  | { type: 'open'; filename: string; buffer: ArrayBuffer }
  | { type: 'read'; path: string; requestId: number };

export type Hdf5WorkerErrorCode = 'not-ready' | 'not-dataset' | 'parse-failed' | 'read-failed';

export type Hdf5WorkerResponse =
  | { type: 'ready'; root: Hdf5TreeNode; nodeCount: number; truncated: boolean; overview?: AnnDataOverview }
  | { type: 'detail'; requestId: number; detail: Hdf5DatasetDetail }
  | {
      type: 'error';
      requestId?: number;
      code: Hdf5WorkerErrorCode;
    };
