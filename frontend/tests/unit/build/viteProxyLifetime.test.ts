import { EventEmitter } from 'node:events';
import { describe, expect, it } from 'vitest';
import { bindProxyRequestLifetime, bindProxySocketLifetime } from '../../../vite.config';

class FakeRequest extends EventEmitter {
  destroyed = false;

  destroy(): this {
    this.destroyed = true;
    this.emit('close');
    return this;
  }
}

class FakeResponse extends EventEmitter {
  writableEnded = false;
}

class FakeSocket extends EventEmitter {
  destroyed = false;

  destroy(): this {
    this.destroyed = true;
    this.emit('close');
    return this;
  }
}

describe('source backend proxy resource lifetime', () => {
  it('destroys a pending upstream HTTP request when the browser response closes', () => {
    const request = new FakeRequest();
    const response = new FakeResponse();

    bindProxyRequestLifetime(request, response);
    response.emit('close');

    expect(request.destroyed).toBe(true);
    expect(request.listenerCount('close')).toBe(0);
    expect(response.listenerCount('close')).toBe(0);
    expect(response.listenerCount('finish')).toBe(0);
  });

  it('keeps a completed HTTP response from destroying its upstream request', () => {
    const request = new FakeRequest();
    const response = new FakeResponse();

    bindProxyRequestLifetime(request, response);
    response.writableEnded = true;
    response.emit('finish');
    response.emit('close');

    expect(request.destroyed).toBe(false);
    expect(request.listenerCount('close')).toBe(0);
  });

  it('destroys the other WebSocket half and removes listeners when either peer closes', () => {
    const downstream = new FakeSocket();
    const upstream = new FakeSocket();

    bindProxySocketLifetime(downstream, upstream);
    upstream.emit('close');

    expect(downstream.destroyed).toBe(true);
    expect(downstream.listenerCount('close')).toBe(0);
    expect(downstream.listenerCount('error')).toBe(0);
    expect(upstream.listenerCount('close')).toBe(0);
    expect(upstream.listenerCount('error')).toBe(0);
  });
});
