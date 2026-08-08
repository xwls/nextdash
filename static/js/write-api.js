(function (global) {
    'use strict';

    function readMeta(name) {
        return document.querySelector(`meta[name="${name}"]`)?.content?.trim() || '';
    }

    function readWriteToken() {
        return readMeta('nextdash-write-token');
    }

    function readCSRFToken() {
        return readMeta('nextdash-csrf-token');
    }

    function mergeHeaders(extraHeaders) {
        const headers = {};
        if (extraHeaders) {
            new Headers(extraHeaders).forEach((value, key) => {
                headers[key] = value;
            });
        }
        const token = readWriteToken();
        const csrf = readCSRFToken();
        if (token) headers['X-NextDash-Token'] = token;
        if (csrf) headers['X-NextDash-CSRF'] = csrf;
        return headers;
    }

    global.nextDashWriteHeaders = mergeHeaders;

    const nativeFetch = global.fetch.bind(global);

    /** fetch() with the configured Write Token and Session CSRF token merged. */
    global.nextDashFetch = function nextDashFetch(url, init) {
        const options = { ...(init || {}) };
        options.headers = mergeHeaders(options.headers);
        return nativeFetch(url, options);
    };

    // Existing dashboard modules include many direct fetch() calls. Automatically
    // attach authentication headers to every same-origin unsafe request so all
    // current and future writes receive consistent CSRF protection.
    global.fetch = function authenticatedFetch(input, init) {
        const options = { ...(init || {}) };
        const method = String(options.method || input?.method || 'GET').toUpperCase();
        let target;
        try {
            target = new URL(typeof input === 'string' || input instanceof URL ? input : input.url, global.location.href);
        } catch {
            return nativeFetch(input, init);
        }
        if (target.origin === global.location.origin && ['POST', 'PUT', 'PATCH', 'DELETE'].includes(method)) {
            options.headers = mergeHeaders(options.headers || input?.headers);
            return nativeFetch(input, options);
        }
        return nativeFetch(input, init);
    };
})(typeof window !== 'undefined' ? window : globalThis);
