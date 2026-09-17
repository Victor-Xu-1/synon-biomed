/**
 * Type declarations for pptx2json
 */
declare module 'pptx2json' {
  export default class PPTX2Json {
    constructor();
    toJson(file_path: string): Promise<unknown>;
    toPPTX(json: unknown, options?: { file?: string }): Promise<Buffer>;
    getMaxSlideIds(json: unknown): { id: number; rid: number };
    getSlideLayoutTypeHash(json: unknown): Record<string, string>;
  }
}
