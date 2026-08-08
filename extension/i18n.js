/* Extension UI translations (en / nl / de / fr / zh-CN / zh-TW) */

const EXT_LANGUAGE_MAP = new Map([
    ['en', 'en'],
    ['nl', 'nl'],
    ['de', 'de'],
    ['fr', 'fr'],
    ['zh', 'zh-CN'],
    ['zh-cn', 'zh-CN'],
    ['zh-hans', 'zh-CN'],
    ['zh-sg', 'zh-CN'],
    ['zh-my', 'zh-CN'],
    ['zh-tw', 'zh-TW'],
    ['zh-hant', 'zh-TW'],
    ['zh-hk', 'zh-TW'],
    ['zh-mo', 'zh-TW'],
]);
let extStrings = {};
let extLang = 'en';

function extNormalizeLang(code) {
    const lower = String(code || 'en').toLowerCase();
    const direct = EXT_LANGUAGE_MAP.get(lower);
    if (direct) return direct;

    const parts = lower.split('-');
    if (parts[0] === 'zh') {
        if (parts.includes('hant')) return 'zh-TW';
        if (parts.includes('hans')) return 'zh-CN';
        return parts.some((part) => ['tw', 'hk', 'mo'].includes(part)) ? 'zh-TW' : 'zh-CN';
    }
    return EXT_LANGUAGE_MAP.get(parts[0]) || 'en';
}

function extInterpolate(text, vars) {
    if (!vars) return text;
    return String(text).replace(/\{(\w+)\}/g, (_, key) => (vars[key] != null ? String(vars[key]) : `{${key}}`));
}

function extT(key, fallback, vars) {
    const raw = extStrings[key] ?? fallback ?? key;
    return extInterpolate(raw, vars);
}

async function extLoadLocaleFile(lang) {
    const url = chrome.runtime.getURL(`locales/${lang}.json`);
    const res = await fetch(url);
    if (!res.ok) throw new Error(`locale ${lang}`);
    return res.json();
}

async function resolveExtensionLang() {
    const stored = await chrome.storage.sync.get(['extensionLocale', 'serverUrl']);
    let lang = stored.extensionLocale;

    if (!lang && typeof chrome !== 'undefined' && chrome.i18n?.getUILanguage) {
        lang = chrome.i18n.getUILanguage();
    }
    if (!lang && typeof navigator !== 'undefined' && navigator.language) {
        lang = navigator.language;
    }

    return extNormalizeLang(lang || 'en');
}

async function loadExtensionLocale(lang) {
    const normalized = extNormalizeLang(lang);
    try {
        extStrings = await extLoadLocaleFile(normalized);
        extLang = normalized;
    } catch (e) {
        extStrings = await extLoadLocaleFile('en');
        extLang = 'en';
    }
    return extLang;
}

/** Service worker / background — no DOM. */
async function initExtensionI18nBackground() {
    const lang = await resolveExtensionLang();
    return loadExtensionLocale(lang);
}

async function initExtensionI18n() {
    await initExtensionI18nBackground();
    if (typeof document !== 'undefined' && document.documentElement) {
        document.documentElement.lang = extLang;
        applyExtensionI18n();
    }
}

function applyExtensionI18n() {
    document.querySelectorAll('[data-i18n]').forEach((el) => {
        const key = el.getAttribute('data-i18n');
        if (!key) return;
        el.textContent = extT(key, el.textContent);
    });

    document.querySelectorAll('[data-i18n-placeholder]').forEach((el) => {
        const key = el.getAttribute('data-i18n-placeholder');
        el.placeholder = extT(key, el.placeholder);
    });

    document.querySelectorAll('[data-i18n-title]').forEach((el) => {
        const key = el.getAttribute('data-i18n-title');
        el.title = extT(key, el.title);
    });

    document.querySelectorAll('[data-i18n-aria]').forEach((el) => {
        const key = el.getAttribute('data-i18n-aria');
        el.setAttribute('aria-label', extT(key, el.getAttribute('aria-label') || ''));
    });
}
