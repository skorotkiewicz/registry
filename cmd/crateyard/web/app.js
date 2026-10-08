'use strict';

(() => {
  const $ = (id) => document.getElementById(id);
  let token = '';
  let catalog = { url: '', packages: [] };
  let generation = 0;
  let selected = '';
  let view = 'index';
  const key = (pkg) => `${pkg.kind}/${pkg.name}`;
  const publisher = (pkg) => pkg.publisher || 'unknown';
  const loadingButtons = [$('login-form').querySelector('button'), $('refresh')];

  function element(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  }

  function showView(id) {
    view = id;
    for (const name of ['index', 'package-detail', 'client-setup']) $(name).hidden = name !== id;
    for (const [tab, target] of [['index-tab', 'index'], ['config-tab', 'client-setup']]) {
      if (target === id) $(tab).setAttribute('aria-current', 'page');
      else $(tab).removeAttribute('aria-current');
    }
  }

  function tags(cell, labels) {
    for (const label of labels) cell.append(element('span', label, 'tag'));
    if (!labels.length) cell.append(element('span', 'none', 'muted'));
  }

  function openPackage(pkg, focus = true) {
    selected = key(pkg);
    $('package-title').textContent = pkg.name;
    $('package-kind').textContent = pkg.kind;
    $('package-publisher').textContent = publisher(pkg);
    $('package-count').textContent = pkg.versions.length;
    $('versions').replaceChildren();
    for (const version of pkg.versions) {
      const row = element('tr');
      row.append(element('td', version.version));
      const state = element('td');
      tags(state, version.tags);
      if (version.yanked) state.append(element('span', 'yanked', 'tag yanked'));
      row.append(state);
      const install = element('td');
      if (version.yanked) {
        install.append(element('span', 'Yanked. Choose another version.', 'muted'));
      } else {
        const command = pkg.kind === 'npm'
          ? `npm install ${pkg.name}@${version.version} --registry ${catalog.url}/npm/ --no-audit`
          : `${pkg.name} = { version = "=${version.version}", registry = "selfhost" }`;
        const line = element('div', undefined, 'install-command');
        line.append(element('pre', command));
        const copy = element('button', 'copy', 'copy');
        copy.type = 'button';
        copy.setAttribute('aria-label', `Copy install command for ${pkg.name} ${version.version}`);
        copy.addEventListener('click', async () => {
          try {
            if (!navigator.clipboard) throw new Error('Clipboard unavailable');
            await navigator.clipboard.writeText(command);
            $('status').textContent = `Copied install command for ${pkg.name} ${version.version}.`;
          } catch {
            $('status').textContent = 'Clipboard unavailable. Select the command and copy it manually.';
          }
        });
        line.append(copy);
        install.append(line);
      }
      row.append(install);
      $('versions').append(row);
    }
    showView('package-detail');
    if (focus) $('package-title').focus();
  }

  function render() {
    const search = $('search').value.trim().toLowerCase();
    const kind = $('kind').value;
    const packages = catalog.packages.filter((pkg) =>
      `${pkg.name} ${publisher(pkg)}`.toLowerCase().includes(search) && (kind === 'all' || pkg.kind === kind));
    $('count').textContent = `${packages.length} / ${catalog.packages.length} packages`;
    $('packages').replaceChildren();
    if (!packages.length) {
      const row = element('tr');
      const cell = element('td', catalog.packages.length
        ? 'No matching packages.' : 'Nothing buried here yet. Publish a package with Cargo or npm.', 'empty');
      cell.colSpan = 5;
      row.append(cell);
      $('packages').append(row);
    }
    for (const pkg of packages) {
      const row = element('tr');
      const name = element('td');
      const link = element('button', pkg.name, 'package-link');
      link.type = 'button';
      link.dataset.key = key(pkg);
      link.addEventListener('click', () => openPackage(pkg));
      name.append(link);
      row.append(name, element('td', pkg.kind, 'muted'), element('td', publisher(pkg)), element('td', pkg.versions.length));
      const labels = [...new Set(pkg.versions.flatMap((version) => version.tags))].sort();
      const cell = element('td');
      tags(cell, labels);
      row.append(cell);
      $('packages').append(row);
    }
    if (view === 'package-detail') {
      const pkg = catalog.packages.find((entry) => key(entry) === selected);
      if (pkg) openPackage(pkg, false);
      else showView('index');
    }
  }

  function backToIndex() {
    showView('index');
    const link = [...$('packages').querySelectorAll('button')].find((button) => button.dataset.key === selected);
    (link || $('search')).focus();
  }

  function disconnect() {
    generation++;
    token = '';
    selected = '';
    catalog = { url: '', packages: [] };
    $('token').value = '';
    $('packages').replaceChildren();
    $('versions').replaceChildren();
    for (const id of ['cargo-config', 'npm-config', 'npm-publish', 'count', 'endpoint', 'session', 'package-title', 'package-kind', 'package-publisher', 'package-count']) $(id).textContent = '';
    $('browser').hidden = true;
    $('login').hidden = false;
    showView('index');
    loadingButtons.forEach((button) => { button.disabled = false; });
    $('status').textContent = 'Disconnected. Token forgotten.';
    $('token').focus();
  }

  async function refresh() {
    const current = ++generation;
    $('status').textContent = 'Reading package metadata...';
    loadingButtons.forEach((button) => { button.disabled = true; });
    try {
      const response = await fetch('/api/packages', {
        headers: token ? { Authorization: `Bearer ${token}` } : {}, cache: 'no-store', credentials: 'omit',
      });
      if (current !== generation) return;
      if (response.status === 401) {
        disconnect();
        $('status').textContent = 'Token not accepted. Check it and try again.';
        return;
      }
      if (!response.ok) throw new Error('Could not read packages. Check the server and try refresh.');
      const result = await response.json();
      if (current !== generation) return;
      catalog = result;
      const wasConnected = !$('browser').hidden;
      $('login').hidden = true;
      $('browser').hidden = false;
      $('token').value = '';
      $('session').textContent = token ? 'token session' : 'anonymous';
      $('endpoint').textContent = catalog.url;
      $('cargo-config').textContent = `[registries.selfhost]\nindex = "sparse+${catalog.url}/cargo/"\ncredential-provider = "cargo:token"`;
      const url = new URL(catalog.url);
      $('npm-config').textContent = `@selfhost:registry=${catalog.url}/npm/\n//${url.host}/npm/:_authToken=\${REGISTRY_TOKEN}`;
      $('npm-publish').textContent = `npm publish --registry ${catalog.url}/npm/`;
      if (!wasConnected) showView('index');
      render();
      $('status').textContent = token ? 'Connected. Refresh after publishing.' : 'Anonymous read access. Publishing requires a user token.';
      if (!wasConnected) $('search').focus();
    } catch (error) {
      if (current === generation) $('status').textContent = error.message;
    } finally {
      if (current === generation) loadingButtons.forEach((button) => { button.disabled = false; });
    }
  }

  $('login-form').addEventListener('submit', (event) => {
    event.preventDefault();
    token = $('token').value;
    $('token').value = '';
    refresh();
  });
  $('refresh').addEventListener('click', refresh);
  $('disconnect').addEventListener('click', disconnect);
  $('search').addEventListener('input', render);
  $('kind').addEventListener('change', render);
  $('browse-public').addEventListener('click', () => { token = ''; refresh(); });
  $('index-tab').addEventListener('click', backToIndex);
  $('back-index').addEventListener('click', backToIndex);
  for (const id of ['config-tab', 'package-config']) $(id).addEventListener('click', () => showView('client-setup'));
  document.addEventListener('keydown', (event) => {
    if (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey && !$('browser').hidden && !['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement.tagName)) {
      event.preventDefault();
      showView('index');
      $('search').focus();
    }
    if (event.key === 'Escape' && view === 'package-detail') backToIndex();
  });

  fetch('/cargo/config.json', { cache: 'no-store', credentials: 'omit' })
    .then((response) => { if (!response.ok) throw new Error('Could not read registry settings.'); return response.json(); })
    .then((settings) => {
      const isPublic = settings['auth-required'] === false;
      $('access').textContent = isPublic ? 'public reads' : 'private / token required';
      if (isPublic) {
        $('browse-public').hidden = false;
        if (!token && $('browser').hidden) refresh();
      }
    })
    .catch((error) => { if (!token) $('status').textContent = error.message; });
})();
