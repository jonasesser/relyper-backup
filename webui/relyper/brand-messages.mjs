// relyper: builds the UI message catalogs with the Relyper Backup name.
//
// Upstream keeps its translations in messages/<locale>.json and changes them
// often. Instead of editing those files (and resolving conflicts on every
// upstream merge), this script copies them to .relyper-messages/ and replaces
// the product name there. project.inlang/settings.json points paraglide at the
// generated directory. Run automatically by the compile-i18n, build and start
// scripts in package.json.
//
// Keys in KEEP_UPSTREAM_NAME talk about the upstream project itself (its
// documentation, its releases) and keep the name "Backrest".
import { readdirSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const PRODUCT_NAME = 'Relyper Backup';

export const KEEP_UPSTREAM_NAME = new Set([
  'dashboard_getting_started_check', // links to the Backrest GitHub project
  'dashboard_getting_started_overview_d_b', // "the Backrest wiki" link
  'add_plan_modal_see_guide_link', // links to the Backrest guide
  'add_repo_modal_guide_link_text', // links to the Backrest guide
]);

// Replaces the name in prose. Paths and identifiers such as
// "$HOME/.backrest/config.json" stay untouched.
export const brandText = (value) =>
  value
    .replaceAll('Backrest', PRODUCT_NAME)
    .replace(/backrest-server/g, 'relyper-backup')
    .replace(/(^|[\s(])backrest(?=[\s!,.;:)]|$)/g, `$1${PRODUCT_NAME}`);

export const brandMessages = (messages) => {
  const out = {};
  for (const [key, value] of Object.entries(messages)) {
    out[key] =
      typeof value === 'string' && !KEEP_UPSTREAM_NAME.has(key)
        ? brandText(value)
        : value;
  }
  return out;
};

const main = () => {
  const root = join(dirname(fileURLToPath(import.meta.url)), '..');
  const src = join(root, 'messages');
  const dst = join(root, '.relyper-messages');
  mkdirSync(dst, { recursive: true });
  let files = 0;
  for (const file of readdirSync(src)) {
    if (!file.endsWith('.json')) continue;
    const messages = JSON.parse(readFileSync(join(src, file), 'utf8'));
    writeFileSync(join(dst, file), JSON.stringify(brandMessages(messages), null, 2) + '\n');
    files++;
  }
  console.log(`relyper: branded ${files} message catalogs in ${dst}`);
};

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  main();
}
