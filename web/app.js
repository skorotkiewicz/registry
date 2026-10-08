'use strict';

(() => {
  const $ = (id) => document.getElementById(id);
  let token = '';
  let catalog = { url: '', packages: [] };
  let generation = 0;

  function element(tag, text, className) {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  }

  function render() {
    const search = $('search').value.toLowerCase();
    const kind = $('kind').value;
    const packages = catalog.packages.filter((p) =>
      p.name.toLowerCase().includes(search) && (kind === 'all' || p.kind === kind));
    $('count').textContent = `${packages.length} of ${catalog.packages.length} packages`;
    $('packages').replaceChildren();
    if (!packages.length) {
      $('packages').append(element('p', catalog.packages.length
        ? 'No matching packages.' : 'No packages yet. Publish your first package with Cargo or npm.'));
    }
    for (const pkg of packages) {
      const card = element('article', undefined, 'package');
      card.append(element('span', pkg.kind === 'cargo' ? 'Rust crate' : 'npm', 'badge'));
      card.append(element('h2', pkg.name));
      const details = element('details');
      details.append(element('summary', `${pkg.versions.length} versions and install commands`));
      const versions = element('ul', undefined, 'versions');
      for (const version of pkg.versions) {
        const item = element('li');
        item.append(element('span', version.version, 'version'));
        for (const tag of version.tags) item.append(element('span', tag, 'badge'));
        if (version.yanked) {
          item.append(element('span', 'yanked', 'badge yanked'));
        } else {
          const command = pkg.kind === 'npm'
            ? `npm install ${pkg.name}@${version.version} --registry ${catalog.url}/npm/ --no-audit`
            : `${pkg.name} = { version = "=${version.version}", registry = "selfhost" }`;
          item.append(element('pre', command));
        }
        versions.append(item);
      }
      details.append(versions);
      card.append(details);
      $('packages').append(card);
    }
  }

  function disconnect() {
    generation++;
    token = '';
    catalog = { url: '', packages: [] };
    $('token').value = '';
    $('packages').replaceChildren();
    for (const id of ['cargo-config', 'npm-config', 'npm-publish', 'count']) $(id).textContent = '';
    $('browser').hidden = true;
    $('login').hidden = false;
    $('status').textContent = 'Disconnected.';
    $('token').focus();
  }

  async function refresh() {
    const current = ++generation;
    $('status').textContent = 'Loading packages...';
    const buttons = [$('login-form').querySelector('button'), $('refresh')];
    buttons.forEach((button) => { button.disabled = true; });
    try {
      const response = await fetch('/api/packages', {
        headers: { Authorization: `Bearer ${token}` }, cache: 'no-store', credentials: 'omit',
      });
      if (current !== generation) return;
      if (response.status === 401) {
        disconnect();
        $('status').textContent = 'Token not accepted. Check it and try again.';
        return;
      }
      if (!response.ok) throw new Error('Could not load packages. Check the server and try Refresh.');
      const result = await response.json();
      if (current !== generation) return;
      catalog = result;
      $('login').hidden = true;
      $('browser').hidden = false;
      $('token').value = '';
      $('cargo-config').textContent = `[registries.selfhost]\nindex = "sparse+${catalog.url}/cargo/"\ncredential-provider = "cargo:token"`;
      const url = new URL(catalog.url);
      $('npm-config').textContent = `@selfhost:registry=${catalog.url}/npm/\n//${url.host}/npm/:_authToken=\${REGISTRY_TOKEN}`;
      $('npm-publish').textContent = `npm publish --registry ${catalog.url}/npm/`;
      render();
      $('status').textContent = 'Connected. Refresh after publishing to see new packages.';
    } catch (error) {
      if (current === generation) $('status').textContent = error.message;
    } finally {
      buttons.forEach((button) => { button.disabled = false; });
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
})();
