import type { NextConfig } from "next";

// The Go API server. The browser never calls it directly: /api/v1/* on this
// app is proxied to it, so the backend needs no CORS configuration.
const apiURL = process.env.SYNERGY_API_URL ?? "http://127.0.0.1:8080";

const nextConfig: NextConfig = {
  poweredByHeader: false,
  async rewrites() {
    return [{ source: "/api/v1/:path*", destination: `${apiURL}/api/v1/:path*` }];
  },
};

export default nextConfig;
