# Vendored inlang plugins

`plugin-message-format-4.6.2.js` is the unmodified `dist/index.js` of
[`@inlang/plugin-message-format@4.6.2`](https://www.npmjs.com/package/@inlang/plugin-message-format)
(MIT, see `plugin-message-format-4.6.2.LICENSE`).

Upstream Backrest loads this plugin from `cdn.jsdelivr.net` with a floating
major version (`@4`) every time the translations are compiled. Relyper Backup
vendors it so that builds are reproducible, work offline and do not execute
unpinned remote code. Its SHA-256 is recorded in `checksums.txt`.

To update: `npm pack @inlang/plugin-message-format@<version>`, copy
`package/dist/index.js` here under the new name, update
`../../project.inlang/settings.json` and `checksums.txt`.
