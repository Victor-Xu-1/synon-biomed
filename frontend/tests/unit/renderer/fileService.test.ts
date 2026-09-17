/**
 * @vitest-environment node
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CSRF_HEADER_NAME } from '@/renderer/services/csrf';
import { uploadFileViaHttp } from '@/renderer/services/FileService';

type XhrListener = () => void;

class FakeXMLHttpRequest {
  static instances: FakeXMLHttpRequest[] = [];

  method = '';
  url = '';
  status = 0;
  statusText = '';
  responseText = '';
  sentBody: unknown;
  headers = new Map<string, string>();
  upload = { addEventListener: vi.fn() };

  private listeners: Record<string, XhrListener> = {};

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }

  addEventListener(name: string, listener: XhrListener) {
    this.listeners[name] = listener;
  }

  setRequestHeader(name: string, value: string) {
    this.headers.set(name, value);
  }

  abort() {
    this.listeners.abort?.();
  }

  send(body: unknown) {
    this.sentBody = body;
    FakeXMLHttpRequest.instances.push(this);
  }

  respond(status: number, responseText: string, statusText = '') {
    this.status = status;
    this.statusText = statusText;
    this.responseText = responseText;
    this.listeners.load?.();
  }
}

describe('FileService.uploadFileViaHttp', () => {
  beforeEach(() => {
    FakeXMLHttpRequest.instances = [];
    vi.stubGlobal('XMLHttpRequest', FakeXMLHttpRequest);
    vi.stubGlobal('window', {
      location: { href: 'http://localhost:8765/#/chat', origin: 'http://localhost:8765' },
    });
    vi.stubGlobal('location', {
      href: 'http://localhost:8765/#/chat',
      origin: 'http://localhost:8765',
    });
    vi.stubGlobal('document', { cookie: 'theme=dark; synon_csrf=composer-upload-csrf-token' });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('uses the authenticated multipart upload contract with the same-origin CSRF token', async () => {
    const file = new File(['compound,value\naspirin,1\n'], 'input.csv', { type: 'text/csv' });
    const pending = uploadFileViaHttp(file, 'conversation-a');
    const xhr = FakeXMLHttpRequest.instances[0];

    expect(xhr).toBeDefined();
    expect(xhr.method).toBe('POST');
    expect(xhr.url).toContain('/api/fs/upload');
    expect(xhr.headers.get(CSRF_HEADER_NAME)).toBe('composer-upload-csrf-token');
    expect(xhr.sentBody).toBeInstanceOf(FormData);
    const form = xhr.sentBody as FormData;
    expect((form.get('file') as File).name).toBe('input.csv');
    expect(form.get('conversation_id')).toBe('conversation-a');

    xhr.respond(200, JSON.stringify({ success: true, data: '/private/uploads/input.csv' }));
    await expect(pending).resolves.toBe('/private/uploads/input.csv');
  });
});
