import DOMPurify from 'dompurify';

export const HTML_PREVIEW_SANDBOX = 'allow-scripts';

export async function readHtmlResponseText(response: Response): Promise<string> {
  const charset = /charset\s*=\s*["']?([^;\s"']+)/i.exec(response.headers.get('Content-Type') ?? '')?.[1] ?? 'utf-8';
  return new TextDecoder(charset).decode(await response.arrayBuffer());
}

export function isolatedPreviewMessage(
  event: MessageEvent<unknown>,
  frame: HTMLIFrameElement | null,
  instance: string
): Record<string, unknown> | null {
  if (!frame?.contentWindow || event.source !== frame.contentWindow) return null;
  if (!event.data || typeof event.data !== 'object') return null;
  const envelope = event.data as Record<string, unknown>;
  if (envelope.__synonPreviewInstance !== instance || !envelope.payload || typeof envelope.payload !== 'object')
    return null;
  return envelope.payload as Record<string, unknown>;
}

export function sendPreviewScripts(frame: HTMLIFrameElement | null, instance: string, scripts: string[]): void {
  frame?.contentWindow?.postMessage({ __synonPreviewInstance: instance, scripts }, '*');
}

// All previews have an opaque origin. Passive source documents additionally
// discard active markup and permit only the product's nonce-bound bridge.
// The mode does not grant application-origin access, even for generated charts.
export function isolatedHtmlDocument(content: string, instance: string, passive: boolean): string {
  const document = new DOMParser().parseFromString(
    passive
      ? DOMPurify.sanitize(content, {
          WHOLE_DOCUMENT: true,
          FORBID_TAGS: ['script', 'iframe', 'object', 'embed', 'form', 'base', 'meta', 'link'],
          FORBID_ATTR: ['srcdoc', 'nonce'],
        })
      : content,
    'text/html'
  );
  document.querySelectorAll('base, meta[http-equiv]').forEach((element) => element.remove());
  const nonce = crypto.randomUUID().replaceAll('-', '');
  const policy = document.createElement('meta');
  policy.httpEquiv = 'Content-Security-Policy';
  policy.content = passive
    ? `default-src 'none'; script-src 'nonce-${nonce}'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; base-uri 'none'; form-action 'none'`
    : `default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval' https: data: blob:; style-src 'unsafe-inline' https:; img-src https: data: blob:; font-src https: data:; connect-src https:; worker-src blob:; base-uri 'none'; form-action 'none'`;
  const bridge = document.createElement('script');
  bridge.nonce = nonce;
  bridge.textContent = `(() => {
    const instance = ${JSON.stringify(instance)};
    const nonce = ${JSON.stringify(nonce)};
    const post = parent.postMessage.bind(parent);
    const send = payload => post({__synonPreviewInstance: instance, payload}, '*');
    document.currentScript?.remove();
    addEventListener('message', event => {
      if (!event.isTrusted || event.source !== parent || event.data?.__synonPreviewInstance !== instance || !Array.isArray(event.data.scripts)) return;
      for (const source of event.data.scripts) {
        if (typeof source !== 'string') continue;
        const script = document.createElement('script');
        script.nonce = nonce;
        script.textContent = '(() => { const postToHost = payload => parent.postMessage({__synonPreviewInstance:' + JSON.stringify(instance) + ',payload}, "*");' + source.replaceAll('window.parent.postMessage(', 'postToHost(') + '\\n})();';
        (document.head || document.documentElement).appendChild(script);
        script.remove();
      }
    });
    addEventListener('mouseup', () => setTimeout(() => {
      const selection = getSelection();
      const text = selection && !selection.isCollapsed ? selection.toString().trim().slice(0, 65536) : '';
      if (!text || !selection.rangeCount) { send({selection:null}); return; }
      const rect = selection.getRangeAt(0).getBoundingClientRect();
      send({selection:{text,x:rect.left+rect.width/2,y:rect.bottom}});
    }, 20));
  })();`;
  document.head.prepend(policy);
  // Browsers hide nonce attributes when connected nodes are serialized. Add
  // the trusted bridge after serializing the inert source document so its
  // nonce survives srcDoc without ever authorizing a source-provided script.
  const bridgeMarkup = '<script nonce="' + nonce + '">' + bridge.textContent + '</script>';
  return (
    '<!doctype html>\n' + document.documentElement.outerHTML.replace(policy.outerHTML, policy.outerHTML + bridgeMarkup)
  );
}
