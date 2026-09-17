/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

interface InspectMessages {
  copySuccess: string;
}

export interface HtmlElementAnnotationMarker {
  id: string;
  label: string;
  text: string;
  selector: string;
}

const DEFAULT_MESSAGES: InspectMessages = {
  copySuccess: '✓ Copied HTML snippet',
};

/**
 * Generate the script injected by the HTML element inspector.
 * Generate HTML inspect mode injection script
 *
 * @param inspectMode - Whether inspector mode is enabled
 * @param messages - Custom notification messages
 * @returns Injected script source
 */
export function generateInspectScript(inspectMode: boolean, messages: InspectMessages = DEFAULT_MESSAGES): string {
  const copySuccess = JSON.stringify(messages.copySuccess);
  return `
    (function() {

      // Remove old inspector styles and listeners.
      const oldStyle = document.getElementById('inspect-mode-style');
      if (oldStyle) oldStyle.remove();

      const oldOverlay = document.getElementById('inspect-mode-overlay');
      if (oldOverlay) oldOverlay.remove();

      const oldMenu = document.getElementById('inspect-mode-menu');
      if (oldMenu) oldMenu.remove();

      // Remove old event listeners.
      const oldListeners = window.__inspectModeListeners || {};
      if (oldListeners.mousemove) {
        document.removeEventListener('mousemove', oldListeners.mousemove);
      }
      if (oldListeners.click) {
        document.removeEventListener('click', oldListeners.click);
      }

      if (!${inspectMode}) {
        // Remove inspector elements when the mode is disabled.
        document.body.style.cursor = '';
        window.__inspectModeListeners = null;
        return;
      }

      // Add inspector styles.
      const style = document.createElement('style');
      style.id = 'inspect-mode-style';
      style.textContent = \`
        .inspect-overlay {
          position: fixed;
          pointer-events: none;
          background: rgba(59, 130, 246, 0.1);
          border: 2px solid #3b82f6;
          z-index: 999999;
          transition: all 0.1s ease;
        }
      \`;
      document.head.appendChild(style);

      // Create the highlight overlay.
      const overlay = document.createElement('div');
      overlay.id = 'inspect-mode-overlay';
      overlay.className = 'inspect-overlay';
      overlay.style.display = 'none';
      document.body.appendChild(overlay);

      let currentElement = null;

      // Show the notification.
      const showNotification = (message) => {
        const notification = document.createElement('div');
        notification.textContent = message;
        notification.style.cssText = \`
          position: fixed;
          top: 20px;
          right: 20px;
          background: #10b981;
          color: white;
          padding: 12px 20px;
          border-radius: 6px;
          font-size: 14px;
          z-index: 1000000;
          box-shadow: 0 4px 6px rgba(0,0,0,0.1);
        \`;
        document.body.appendChild(notification);
        setTimeout(() => notification.remove(), 2000);
      };

      // Highlight the element under the pointer.
      const handleMouseMove = (e) => {
        const element = document.elementFromPoint(e.clientX, e.clientY);
        if (element && element !== currentElement && element !== overlay) {
          currentElement = element;
          const rect = element.getBoundingClientRect();
          overlay.style.display = 'block';
          overlay.style.left = rect.left + 'px';
          overlay.style.top = rect.top + 'px';
          overlay.style.width = rect.width + 'px';
          overlay.style.height = rect.height + 'px';
        }
      };

        // Build a concise element label.
      const getSimplifiedTag = (element) => {
        const tagName = element.tagName.toLowerCase();
        const id = element.id ? '#' + element.id : '';
        const className = element.className && typeof element.className === 'string'
          ? '.' + element.className.split(' ').filter(c => c).slice(0, 1).join('.')
          : '';
        return tagName + id + className;
      };

      const escapeCss = (value) => {
        if (window.CSS && typeof window.CSS.escape === 'function') return window.CSS.escape(value);
        return String(value).replace(/[^a-zA-Z0-9_-]/g, '_');
      };

      const getStableSelector = (element) => {
        if (element.id) {
          const selector = '#' + escapeCss(element.id);
          try { if (document.querySelectorAll(selector).length === 1) return selector; } catch (_) {}
        }
        for (const attribute of ['data-testid', 'data-test', 'data-cy', 'name']) {
          const value = element.getAttribute && element.getAttribute(attribute);
          if (!value) continue;
          const selector = element.tagName.toLowerCase() + '[' + attribute + '=' + JSON.stringify(String(value)) + ']';
          try { if (document.querySelectorAll(selector).length === 1) return selector; } catch (_) {}
        }
        const parts = [];
        let current = element;
        while (current && current.nodeType === 1 && current !== document.documentElement && parts.length < 8) {
          let part = current.tagName.toLowerCase();
          const parent = current.parentElement;
          if (parent) {
            const siblings = Array.from(parent.children).filter((item) => item.tagName === current.tagName);
            if (siblings.length > 1) part += ':nth-of-type(' + (siblings.indexOf(current) + 1) + ')';
          }
          parts.unshift(part);
          const selector = parts.join(' > ');
          try { if (document.querySelectorAll(selector).length === 1) return selector; } catch (_) {}
          current = parent;
        }
        return parts.join(' > ') || element.tagName.toLowerCase();
      };

      const describeElement = (element, selector) => {
        const text = String(element.innerText || '').trim().slice(0, 160);
        return selector + (text ? ' — ' + text : '');
      };

      // Send the selected element HTML to the parent window.
      const handleClick = (e) => {
        e.preventDefault();
        e.stopPropagation();

        const element = document.elementFromPoint(e.clientX, e.clientY);
        if (element && element !== overlay) {
          const html = element.outerHTML;
          const tag = getSimplifiedTag(element);
          const selector = getStableSelector(element);
          const descriptor = describeElement(element, selector);
          let elementText = String(element.innerText || '').trim();
          if (elementText.length > 2000) elementText = elementText.slice(0, 2000);
          const rect = element.getBoundingClientRect();
          const payload = {
            html,
            tag,
            selector,
            descriptor,
            text: elementText,
            rect: {
              x: rect.x,
              y: rect.y,
              width: rect.width,
              height: rect.height,
              viewportWidth: window.innerWidth || 1,
              viewportHeight: window.innerHeight || 1
            }
          };

        // Send through console.log so the webview bridge can capture it.
          console.log('__INSPECT_ELEMENT__' + JSON.stringify(payload));
          try {
            window.parent.postMessage({ __SYNON_AI_INSPECT_ELEMENT__: payload }, '*');
          } catch (_) {}

        // Show confirmation.
          showNotification(${copySuccess});
        }
      };

      // Add event listeners.
      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('click', handleClick);

      // Save listener references for later removal.
      window.__inspectModeListeners = {
        mousemove: handleMouseMove,
        click: handleClick
      };

      // Update the pointer style.
      document.body.style.cursor = 'crosshair';
    })();
  `;
}

export function generateHtmlAnnotationHighlightScript(markers: HtmlElementAnnotationMarker[]): string {
  return `
    (function() {
      document.querySelectorAll('[data-synon-ai-html-annotation]').forEach((node) => node.remove());
      const markers = ${JSON.stringify(markers)};
      for (const marker of markers) {
        let target = null;
        try { target = document.querySelector(marker.selector); } catch (_) {}
        if (!target) continue;
        const rect = target.getBoundingClientRect();
        if (rect.width <= 0 && rect.height <= 0) continue;
        const badge = document.createElement('button');
        badge.type = 'button';
        badge.dataset.synonAiHtmlAnnotation = marker.id;
        badge.textContent = marker.label || '•';
        badge.title = marker.text || marker.selector;
        badge.setAttribute('aria-label', 'Annotation ' + (marker.label || marker.id));
        badge.style.cssText = 'position:absolute;z-index:999998;width:20px;height:20px;border:2px solid white;border-radius:50%;background:#165dff;color:white;font:700 10px/16px sans-serif;box-shadow:0 2px 8px rgba(0,0,0,.25);cursor:pointer;padding:0;';
        badge.style.left = Math.max(0, rect.right + window.scrollX - 10) + 'px';
        badge.style.top = Math.max(0, rect.top + window.scrollY - 10) + 'px';
        badge.addEventListener('click', function(event) {
          event.preventDefault();
          event.stopPropagation();
          console.log('__HTML_ANNOTATION_CLICK__' + marker.id);
          try { window.parent.postMessage({ __SYNON_AI_HTML_ANNOTATION_CLICK__: marker.id }, '*'); } catch (_) {}
        }, true);
        document.body.appendChild(badge);
      }
    })();
  `;
}
