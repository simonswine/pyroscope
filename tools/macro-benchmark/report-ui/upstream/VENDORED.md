# Vendored @grafana/flamegraph 13.2.2

`dist/esm/` and `dist/types/` are copied from the npm tarball
`@grafana/flamegraph@13.2.2` (Apache-2.0; `LICENSE_APACHE2`). CommonJS and
source maps are omitted. No changes were made to its ESM implementation.
`dist/esm/FlameGraphContainer.d.mts` is a local TypeScript bridge to the
upstream type declarations.

`icons/` are copied from `@grafana/ui@13.2.2/dist/public/img/icons/`
(Apache-2.0; `ICONS_LICENSE_APACHE2`) and embedded for offline viewing.
The standalone report passes `enableNewUI` to the **upstream** container;
it does not use the old locally modified flamegraph component.

External dependencies are pinned in `../package.json` and `../yarn.lock`.
The report supplies an offline icon fetch adapter, an optional-assistant stub,
a CommonJS scrollbar interop shim, and one report-specific Baseline / Comparison /
Diff selector wrapping the upstream new UI. These are separate from the vendored
package, making future upstream upgrades easier to review.
