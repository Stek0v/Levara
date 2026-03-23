"""Async MCP client for benchmarking Levara."""
import aiohttp
import time
import json
import uuid

try:
    import numpy as np
    HAS_NUMPY = True
except ImportError:
    HAS_NUMPY = False


def _percentile(sorted_data, p):
    """Pure-Python percentile calculation (no numpy needed)."""
    if not sorted_data:
        return 0.0
    k = (len(sorted_data) - 1) * (p / 100.0)
    f = int(k)
    c = f + 1
    if c >= len(sorted_data):
        return sorted_data[f]
    d = k - f
    return sorted_data[f] + d * (sorted_data[c] - sorted_data[f])


class MCPClient:
    """Async MCP JSON-RPC client for Levara benchmarks."""

    def __init__(self, host="10.23.0.53", port=8080):
        self.base_url = f"http://{host}:{port}"
        self.mcp_url = f"{self.base_url}/mcp"
        self.api_url = f"{self.base_url}/api/v1"
        self.token = None
        self.call_log = []  # [(tool, latency_ms, status, timestamp)]

    async def health(self, session):
        """Check if Levara is reachable."""
        try:
            async with session.get(f"{self.base_url}/health", timeout=aiohttp.ClientTimeout(total=10)) as resp:
                return resp.status == 200
        except Exception:
            return False

    async def auth(self, session, username=None, password=None):
        """Register + login, get JWT token.

        If username/password not provided, generates a unique benchmark user.
        Returns auth headers dict.
        """
        if username is None:
            username = f"bench_{uuid.uuid4().hex[:8]}"
        if password is None:
            password = f"BenchPass_{uuid.uuid4().hex[:12]}"

        # Try register (ignore if user exists)
        reg_payload = {"username": username, "password": password, "email": f"{username}@bench.local"}
        try:
            async with session.post(f"{self.api_url}/auth/register", json=reg_payload) as resp:
                await resp.read()
        except Exception:
            pass

        # Login
        login_payload = {"username": username, "password": password}
        async with session.post(f"{self.api_url}/auth/login", json=login_payload) as resp:
            data = await resp.json()
            if resp.status == 200 and "token" in data:
                self.token = data["token"]
            elif resp.status == 200 and "access_token" in data:
                self.token = data["access_token"]
            else:
                raise RuntimeError(f"Auth failed: {resp.status} {data}")

        return self.auth_headers()

    def auth_headers(self):
        """Return Authorization header dict."""
        if not self.token:
            return {}
        return {"Authorization": f"Bearer {self.token}"}

    async def call_tool(self, session, tool_name, arguments, headers=None):
        """Call MCP tool via JSON-RPC, measure latency, log result.

        Returns (result_dict, latency_ms).
        """
        payload = {
            "jsonrpc": "2.0",
            "id": int(time.monotonic() * 1000000),
            "method": "tools/call",
            "params": {
                "name": tool_name,
                "arguments": arguments or {}
            }
        }

        h = dict(headers or {})
        h["Content-Type"] = "application/json"

        start = time.monotonic()
        try:
            async with session.post(self.mcp_url, json=payload, headers=h,
                                    timeout=aiohttp.ClientTimeout(total=30)) as resp:
                latency = (time.monotonic() - start) * 1000
                body = await resp.json()

                if resp.status == 200:
                    status = "ok"
                    result = body.get("result", body)
                else:
                    status = f"error_{resp.status}"
                    result = body
        except aiohttp.ClientError as e:
            latency = (time.monotonic() - start) * 1000
            status = f"error_{type(e).__name__}"
            result = {"error": str(e)}
        except Exception as e:
            latency = (time.monotonic() - start) * 1000
            status = f"error_{type(e).__name__}"
            result = {"error": str(e)}

        self.call_log.append((tool_name, latency, status, time.time()))
        return result, latency

    async def call_api(self, session, method, path, headers=None, **kwargs):
        """Call REST API endpoint, measure latency.

        Returns (response_data, latency_ms, status_code).
        """
        url = f"{self.api_url}{path}"
        h = dict(headers or {})
        h["Content-Type"] = "application/json"

        start = time.monotonic()
        try:
            async with session.request(method, url, headers=h,
                                       timeout=aiohttp.ClientTimeout(total=30),
                                       **kwargs) as resp:
                latency = (time.monotonic() - start) * 1000
                try:
                    data = await resp.json()
                except Exception:
                    data = await resp.text()
                return data, latency, resp.status
        except Exception as e:
            latency = (time.monotonic() - start) * 1000
            return {"error": str(e)}, latency, 0

    def stats(self, tool_name=None):
        """Calculate p50/p95/p99/mean/qps from call_log.

        If tool_name is None, returns stats for all tools grouped by name.
        """
        if tool_name:
            return self._calc_stats(tool_name)

        tools = sorted(set(entry[0] for entry in self.call_log))
        return {t: self._calc_stats(t) for t in tools}

    def _calc_stats(self, tool_name):
        """Stats for a single tool."""
        entries = [(lat, ts) for (t, lat, st, ts) in self.call_log
                   if t == tool_name and st == "ok"]
        errors = sum(1 for (t, _, st, _) in self.call_log
                     if t == tool_name and st != "ok")
        total = sum(1 for (t, _, _, _) in self.call_log if t == tool_name)

        if not entries:
            return {
                "p50": 0, "p95": 0, "p99": 0, "mean": 0,
                "min": 0, "max": 0,
                "calls": total, "errors": errors, "qps": 0
            }

        latencies = sorted(e[0] for e in entries)
        timestamps = [e[1] for e in entries]

        if HAS_NUMPY:
            arr = np.array(latencies)
            p50 = float(np.percentile(arr, 50))
            p95 = float(np.percentile(arr, 95))
            p99 = float(np.percentile(arr, 99))
            mean = float(np.mean(arr))
        else:
            p50 = _percentile(latencies, 50)
            p95 = _percentile(latencies, 95)
            p99 = _percentile(latencies, 99)
            mean = sum(latencies) / len(latencies)

        # QPS: calls / duration
        if len(timestamps) > 1:
            duration = max(timestamps) - min(timestamps)
            qps = len(timestamps) / duration if duration > 0 else 0
        else:
            qps = 0

        return {
            "p50": round(p50, 2),
            "p95": round(p95, 2),
            "p99": round(p99, 2),
            "mean": round(mean, 2),
            "min": round(min(latencies), 2),
            "max": round(max(latencies), 2),
            "calls": total,
            "errors": errors,
            "qps": round(qps, 2)
        }

    def reset(self):
        """Clear call log."""
        self.call_log.clear()
