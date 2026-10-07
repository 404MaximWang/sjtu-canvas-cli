/**
 * mahiro-soft: Lightweight Cloudflare Worker for script distribution and short links.
 * Domain: soft.mahiro.ink
 */

const GITHUB_PROFILE = "https://github.com/404MaximWang";
const GITHUB_REPO = "https://github.com/404MaximWang/sjtu-canvas-cli";
const RAW_INSTALL_URL = "https://raw.githubusercontent.com/404MaximWang/sjtu-canvas-cli/main/install.sh";

// Cache install script on Cloudflare edge for 5 minutes (300 seconds).
const CACHE_TTL = 300;

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const path = url.pathname;
    const userAgent = (request.headers.get("user-agent") || "").toLowerCase();
    const isCommandLine = /curl|wget|httpie|fetch|powershell/i.test(userAgent);

    // Route 1: Explicit install script endpoints
    if (path === "/sjtu-install.sh" || path === "/sjtu.sh") {
      return handleInstallScript(request, ctx);
    }

    // Route 2: Short path /sjtu
    if (path === "/sjtu" || path === "/sjtu/") {
      if (isCommandLine) {
        return handleInstallScript(request, ctx);
      }
      return Response.redirect(GITHUB_REPO, 302);
    }

    // Route 3: Releases shortcut
    if (path === "/sjtu/releases" || path === "/sjtu/releases/latest") {
      return Response.redirect(`${GITHUB_REPO}/releases/latest`, 302);
    }

    // Route 4: Root path
    if (path === "/" || path === "") {
      if (isCommandLine) {
        return new Response(
          "mahiro-soft distribution service\n\nUsage:\n  curl -fsSL https://soft.mahiro.ink/sjtu-install.sh | sh\n",
          {
            status: 200,
            headers: { "Content-Type": "text/plain; charset=utf-8" },
          }
        );
      }
      return Response.redirect(GITHUB_PROFILE, 302);
    }

    // Default 404
    return new Response("404 Not Found\n", {
      status: 404,
      headers: { "Content-Type": "text/plain; charset=utf-8" },
    });
  },
};

/**
 * Fetch and stream the install script from GitHub Raw with Cloudflare Edge caching.
 */
async function handleInstallScript(request, ctx) {
  const cacheKey = new Request(RAW_INSTALL_URL, request);
  const cache = caches.default;

  // Check Cloudflare Edge Cache first
  let response = await cache.match(cacheKey);
  if (!response) {
    const upstream = await fetch(RAW_INSTALL_URL, {
      cf: {
        cacheTtl: CACHE_TTL,
        cacheEverything: true,
      },
    });

    if (!upstream.ok) {
      return new Response(`Error: Failed to fetch install script (upstream status ${upstream.status})\n`, {
        status: 502,
        headers: { "Content-Type": "text/plain; charset=utf-8" },
      });
    }

    // Build cached response with security headers and cache control
    response = new Response(upstream.body, upstream);
    response.headers.set("Content-Type", "text/plain; charset=utf-8");
    response.headers.set("Cache-Control", `public, max-age=${CACHE_TTL}`);
    response.headers.set("X-Content-Type-Options", "nosniff");

    // Put into Edge Cache asynchronously without blocking response
    ctx.waitUntil(cache.put(cacheKey, response.clone()));
  }

  return response;
}
