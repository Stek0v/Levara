import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  async rewrites() {
    return [
      {
        source: '/api/:path*',
        destination: 'http://localhost:8081/api/:path*',
      },
      {
        source: '/health',
        destination: 'http://localhost:8081/health',
      },
      {
        source: '/health/details',
        destination: 'http://localhost:8081/health/details',
      },
    ]
  },
};

export default nextConfig;
