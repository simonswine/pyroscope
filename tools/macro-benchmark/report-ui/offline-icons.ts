// Grafana UI fetches icons from a Grafana server by default. Embed its icon
// assets so the vendored viewer works from a single file:// HTML report.
const icons = import.meta.glob<string>("./upstream/icons/**/*.svg", {
  eager: true,
  query: "?raw",
  import: "default",
});
const originalFetch = window.fetch.bind(window);
window.fetch = (input, init) => {
  const url =
    typeof input === "string"
      ? input
      : input instanceof URL
        ? input.href
        : input.url;
  const match = url.match(/(?:^|\/)public\/build\/img\/icons\/(.+\.svg)$/);
  if (match) {
    const svg =
      icons[`./upstream/icons/${match[1]}`] ??
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="4"/></svg>';
    return Promise.resolve(
      new Response(svg, { headers: { "Content-Type": "image/svg+xml" } }),
    );
  }
  return originalFetch(input, init);
};
