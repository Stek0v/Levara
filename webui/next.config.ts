import type { NextConfig } from "next";

const levaraApiUrl = process.env.LEVARA_API_URL || 'http://localhost:8081'

const nextConfig: NextConfig = {
  async rewrites() {
    return [
      {
        source: '/api/:path*',
        destination: `${levaraApiUrl}/api/:path*`,
      },
      {
        source: '/health',
        destination: `${levaraApiUrl}/health`,
      },
      {
        source: '/health/details',
        destination: `${levaraApiUrl}/health/details`,
      },
    ]
  },
};

export default nextConfig;
