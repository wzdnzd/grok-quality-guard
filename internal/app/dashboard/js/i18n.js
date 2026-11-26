
    const API = '/v0/management/plugins/grok-quality-guard/api';
    const I18N_RESOURCES = '/v0/management/plugins/grok-quality-guard/locales';
    const I18N_RESOURCE_DATA = null; // i18n-resource-data

    const I18N_STORAGE_KEY = 'grok-quality-guard.ui.locale';
    const I18N_SUPPORTED = Object.freeze(['zh-CN', 'zh-TW', 'en']);
    const I18N_INTL = Object.freeze({ 'zh-CN': 'zh-CN', 'zh-TW': 'zh-TW', en: 'en-US' });
    const i18n = (() => {
      let resources = I18N_RESOURCE_DATA && typeof I18N_RESOURCE_DATA === 'object' && !Array.isArray(I18N_RESOURCE_DATA) ? I18N_RESOURCE_DATA : {};

      function normalizeLocale(value) {
        const tag = String(value || '').trim().replace(/_/g, '-').toLowerCase();
        if (!tag) return '';
        if (/^zh-(hant|tw|hk|mo)(?:-|$)/.test(tag)) return 'zh-TW';
        if (/^zh(?:-|$)/.test(tag)) return 'zh-CN';
        if (/^en(?:-|$)/.test(tag)) return 'en';
        return '';
      }

      function storageGet() {
        try { return window.localStorage.getItem(I18N_STORAGE_KEY) || ''; } catch (_) { return ''; }
      }

      function storageSet(value) {
        try { window.localStorage.setItem(I18N_STORAGE_KEY, value); } catch (_) { }
      }

      function detectLocale() {
        const stored = normalizeLocale(storageGet());
        if (stored) return stored;
        const navigatorValue = typeof navigator !== 'undefined' ? navigator : null;
        if (!navigatorValue) return 'zh-CN';
        const candidates = [];
        if (Array.isArray(navigatorValue.languages)) candidates.push(...navigatorValue.languages);
        if (navigatorValue.language) candidates.push(navigatorValue.language);
        for (const candidate of candidates) {
          const locale = normalizeLocale(candidate);
          if (locale) return locale;
        }
        return 'zh-CN';
      }

      let locale = detectLocale();
      let patternRules = [];
      let localizedPatternRules = [];
      const sourceExact = new Map();
      const localizedExact = new Map();
      let suppressMutations = false;
      let resourceRequest = null;
      const textSources = new WeakMap();
      const attributeSources = new WeakMap();
      const appliedTexts = new WeakMap();
      const appliedAttributes = new WeakMap();
      const translatedAttributes = ['placeholder', 'title', 'aria-label'];

      function formatPlural(template, values) {
        return String(template).replace(/\{(\w+):plural\|([^}]+)\}/g, (_match, name, rules) => {
          const value = Number(values && values[name]);
          let category = 'other';
          try { category = new Intl.PluralRules(I18N_INTL[locale] || 'en').select(Number.isFinite(value) ? value : 0); } catch (_) { }
          const branches = {};
          for (const part of String(rules).split('|')) {
            const separator = part.indexOf('=');
            if (separator > 0) branches[part.slice(0, separator).trim()] = part.slice(separator + 1);
          }
          return String(branches[category] ?? branches.other ?? '').replace(/#/g, values && values[name] != null ? String(values[name]) : '');
        });
      }

      function format(template, values) {
        let output = formatPlural(template, values || {});
        output = output.replace(/\{(\w+)\}/g, (_match, name) => values && values[name] != null ? String(values[name]) : _match);
        return output;
      }

      function catalog() {
        return resources[locale] && typeof resources[locale] === 'object' ? resources[locale] : {};
      }

      function compileSourceIndex() {
        sourceExact.clear();
        localizedExact.clear();
        patternRules = [];
        localizedPatternRules = [];
        const localizedCandidates = new Map();
        const localizedPatterns = new Map();
        for (const code of I18N_SUPPORTED) {
          const sourceCatalog = resources[code] && typeof resources[code] === 'object' ? resources[code] : {};
          for (const [key, sourceValue] of Object.entries(sourceCatalog)) {
            const source = String(sourceValue == null ? '' : sourceValue);
            if (!source) continue;
            if (!source.includes('{')) {
              const trimmed = source.trim();
              if (code === 'zh-CN' && !sourceExact.has(trimmed)) sourceExact.set(trimmed, key);
              if (!localizedCandidates.has(trimmed)) localizedCandidates.set(trimmed, new Set());
              localizedCandidates.get(trimmed).add(key);
              continue;
            }
            const names = [];
            let cursor = 0;
            let regex = '^';
            for (const match of source.matchAll(/\{(\w+)\}/g)) {
              regex += source.slice(cursor, match.index).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
              regex += '(.+?)';
              names.push(match[1]);
              cursor = match.index + match[0].length;
            }
            regex += source.slice(cursor).replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$';
            if (code === 'zh-CN') patternRules.push({ regex: new RegExp(regex, 'u'), names, key, source });
            const patternKey = regex + '\u0000' + names.join('\u0000');
            let rule = localizedPatterns.get(patternKey);
            if (!rule) {
              rule = { regex: new RegExp(regex, 'u'), names, keys: new Set(), source };
              localizedPatterns.set(patternKey, rule);
            }
            rule.keys.add(key);
            if (source.length > rule.source.length) rule.source = source;
          }
        }
        for (const [source, keys] of localizedCandidates) {
          if (keys.size === 1) localizedExact.set(source, keys.values().next().value);
        }
        patternRules.sort((left, right) => right.source.length - left.source.length);
        localizedPatternRules = [...localizedPatterns.values()].sort((left, right) => right.source.length - left.source.length);
      }

      function translateSource(source) {
        const text = String(source == null ? '' : source);
        const trimmed = text.trim();
        if (trimmed && Object.prototype.hasOwnProperty.call(catalog(), trimmed)) {
          const leading = text.match(/^\s*/)?.[0] || '';
          const trailing = text.match(/\s*$/)?.[0] || '';
          return leading + t(trimmed) + trailing;
        }
        if (!trimmed || locale === 'zh-CN') return text;
        const leading = text.match(/^\s*/)?.[0] || '';
        const trailing = text.match(/\s*$/)?.[0] || '';
        const exactKey = sourceExact.get(trimmed);
        if (exactKey != null) return leading + t(exactKey) + trailing;
        for (const rule of patternRules) {
          const match = trimmed.match(rule.regex);
          if (!match) continue;
          const values = {};
          rule.names.forEach((name, index) => { values[name] = match[index + 1]; });
          return t(rule.key, values);
        }
        return text;
      }

      function translateLocalizedSource(source) {
        const text = String(source == null ? '' : source);
        const translated = translateSource(text);
        if (translated !== text) return translated;
        const trimmed = text.trim();
        if (!trimmed) return text;
        const leading = text.match(/^\s*/)?.[0] || '';
        const trailing = text.match(/\s*$/)?.[0] || '';
        const exactKey = localizedExact.get(trimmed);
        if (exactKey != null) return leading + t(exactKey) + trailing;
        for (const rule of localizedPatternRules) {
          const match = trimmed.match(rule.regex);
          if (!match) continue;
          const values = {};
          rule.names.forEach((name, index) => { values[name] = match[index + 1]; });
          const rendered = new Set([...rule.keys].map((key) => t(key, values)));
          if (rendered.size === 1) return rendered.values().next().value;
        }
        return text;
      }

      function t(key, values) {
        const message = catalog()[key];
        return format(message == null ? key : message, values || {});
      }

      function hasResources() {
        return I18N_SUPPORTED.every((code) => {
          const catalog = resources[code];
          return catalog && typeof catalog === 'object' && !Array.isArray(catalog) && typeof catalog['ui.overview'] === 'string' && catalog['ui.overview'].trim() !== '';
        });
      }

      async function ensureResources() {
        if (hasResources()) return resources;
        if (resourceRequest) return resourceRequest;
        resourceRequest = (async () => {
          const response = await fetch(I18N_RESOURCES, { headers: { 'Accept-Language': locale } });
          if (!response || !response.ok) throw new Error('i18n_resources_unavailable');
          const payload = await response.json();
          if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('invalid_i18n_resources');
          resources = payload;
          if (!hasResources()) throw new Error('incomplete_i18n_resources');
          compileSourceIndex();
          return resources;
        })();
        try {
          return await resourceRequest;
        } finally {
          resourceRequest = null;
        }
      }

      function skipNode(node) {
        const element = node && (node.nodeType === 1 ? node : node.parentElement);
        return Boolean(element?.closest?.('[data-i18n-skip], script, style, template'));
      }

      function translateTextNode(node, refreshSource) {
        if (!node || node.nodeType !== 3 || skipNode(node)) return;
        const current = node.nodeValue || '';
        if (refreshSource && current === appliedTexts.get(node)) return;
        if (refreshSource || !textSources.has(node)) textSources.set(node, current);
        const source = textSources.get(node);
        if (source == null) return;
        const match = String(source).match(/^(\s*)([\s\S]*?)(\s*)$/);
        if (!match || !match[2]) return;
        const translated = translateLocalizedSource(match[2]);
        const next = match[1] + translated + match[3];
        if (node.nodeValue !== next) node.nodeValue = next;
        appliedTexts.set(node, next);
      }

      function translateAttribute(element, name, refreshSource) {
        if (!element || !element.hasAttribute?.(name) || skipNode(element)) return;
        const current = element.getAttribute(name) || '';
        let applied = appliedAttributes.get(element);
        if (!applied) { applied = new Map(); appliedAttributes.set(element, applied); }
        if (refreshSource && current === applied.get(name)) return;
        let sources = attributeSources.get(element);
        if (!sources) { sources = new Map(); attributeSources.set(element, sources); }
        if (refreshSource || !sources.has(name)) sources.set(name, current);
        const source = sources.get(name);
        if (source == null || !source.trim()) return;
        const translated = translateLocalizedSource(source);
        if (element.getAttribute(name) !== translated) element.setAttribute(name, translated);
        applied.set(name, translated);
      }

      function translateTree(root) {
        if (!root) return;
        suppressMutations = true;
        try {
          if (root.nodeType === 3) translateTextNode(root);
          if (root.nodeType === 1) {
            for (const name of translatedAttributes) {
              if (root.hasAttribute?.(name)) translateAttribute(root, name);
              root.querySelectorAll?.(`[${name}]`).forEach((element) => translateAttribute(element, name));
            }
            if (root.querySelectorAll) root.querySelectorAll('[data-i18n-skip]').forEach((element) => {
              element.setAttribute('data-i18n-skip', element.getAttribute('data-i18n-skip') || '');
            });
          }
          const walker = document.createTreeWalker?.(root, 4, {
            acceptNode(node) { return skipNode(node) ? 2 : 1; }
          });
          if (walker) {
            let node = walker.nextNode();
            while (node) { translateTextNode(node); node = walker.nextNode(); }
          }
          if (document.title) {
            if (!i18nTitleSource) i18nTitleSource = document.title;
            document.title = translateSource(i18nTitleSource);
          }
        } finally {
          suppressMutations = false;
        }
      }

      let i18nTitleSource = '';

      function applyLocale(nextLocale, options) {
        const settings = options || {};
        const normalized = normalizeLocale(nextLocale);
        if (!normalized) return locale;
        locale = normalized;
        compileSourceIndex();
        if (settings.persist) storageSet(locale);
        if (document.documentElement) document.documentElement.lang = locale;
        const select = document.getElementById('uiLanguage');
        if (select) select.value = locale;
        translateTree(document.body);
        if (typeof settings.afterApply === 'function') settings.afterApply(locale);
        return locale;
      }

      function observe() {
        if (typeof MutationObserver !== 'function' || !document.body) return;
        const observer = new MutationObserver((mutations) => {
          if (suppressMutations) return;
          suppressMutations = true;
          try {
            for (const mutation of mutations) {
              if (mutation.type === 'characterData') translateTextNode(mutation.target, true);
              else if (mutation.type === 'attributes' && mutation.attributeName && translatedAttributes.includes(mutation.attributeName)) translateAttribute(mutation.target, mutation.attributeName, true);
              else if (mutation.type === 'childList') mutation.addedNodes.forEach((node) => translateTree(node));
            }
          } finally {
            suppressMutations = false;
          }
        });
        observer.observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: translatedAttributes });
      }

      compileSourceIndex();
      if (document.documentElement) document.documentElement.lang = locale;
      return {
        get locale() { return locale; },
        get intlLocale() { return I18N_INTL[locale] || locale; },
        resources,
        normalizeLocale,
        detectLocale,
        t,
        translateSource,
        translateLocalizedSource,
        applyLocale,
        translateTree,
        observe,
        hasResources,
        ensureResources,
        storageKey: I18N_STORAGE_KEY
      };
    })();

