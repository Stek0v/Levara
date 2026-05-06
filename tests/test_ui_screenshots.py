"""
UI SCREENSHOT TESTS — каждый шаг делает скриншот, Claude анализирует его.
Скриншоты сохраняются в /tmp/levara_screenshots/.
После прогона все скриншоты можно просмотреть для аудита.
"""
import os
import pytest
from playwright.sync_api import Page

BASE = "http://localhost:3000"
SCREENSHOTS_DIR = "/tmp/levara_screenshots"
EMAIL = "admin@levara.dev"
PASSWORD = "admin123456"

os.makedirs(SCREENSHOTS_DIR, exist_ok=True)


def screenshot(page: Page, name: str):
    """Take screenshot and print path for Claude to read."""
    path = os.path.join(SCREENSHOTS_DIR, f"{name}.png")
    page.screenshot(path=path, full_page=True)
    print(f"📸 Screenshot saved: {path}")
    return path


def login(page: Page):
    page.goto(f"{BASE}/auth/login")
    page.wait_for_timeout(2000)
    page.fill('input[name="email"]', EMAIL)
    page.fill('input[name="password"]', PASSWORD)
    page.click('button[type="submit"]')
    page.wait_for_timeout(3000)


# ════════════════════════════════════════
# 1. Login Page
# ════════════════════════════════════════

def test_screenshot_01_login_page(page: Page):
    """Screenshot: Login page before entering credentials."""
    page.goto(f"{BASE}/auth/login")
    page.wait_for_timeout(2000)
    screenshot(page, "01_login_page")


def test_screenshot_02_login_filled(page: Page):
    """Screenshot: Login form with credentials filled."""
    page.goto(f"{BASE}/auth/login")
    page.wait_for_timeout(2000)
    page.fill('input[name="email"]', EMAIL)
    page.fill('input[name="password"]', PASSWORD)
    screenshot(page, "02_login_filled")


def test_screenshot_03_dashboard_after_login(page: Page):
    """Screenshot: Dashboard immediately after login."""
    login(page)
    screenshot(page, "03_dashboard")


# ════════════════════════════════════════
# 2. Dashboard
# ════════════════════════════════════════

def test_screenshot_04_dashboard_full(page: Page):
    """Screenshot: Full dashboard with datasets and notebooks."""
    login(page)
    page.wait_for_timeout(2000)
    screenshot(page, "04_dashboard_full")


# ════════════════════════════════════════
# 3. MCP Status
# ════════════════════════════════════════

def test_screenshot_05_mcp_status(page: Page):
    """Screenshot: MCP status page with all services."""
    login(page)
    page.goto(f"{BASE}/mcp-status")
    page.wait_for_timeout(5000)
    screenshot(page, "05_mcp_status")


def test_screenshot_06_mcp_tools(page: Page):
    """Screenshot: MCP tools section."""
    login(page)
    page.goto(f"{BASE}/mcp-status")
    page.wait_for_timeout(5000)
    # Scroll to tools section
    page.evaluate("window.scrollTo(0, 500)")
    page.wait_for_timeout(1000)
    screenshot(page, "06_mcp_tools")


# ════════════════════════════════════════
# 4. Graph Visualization
# ════════════════════════════════════════

def test_screenshot_07_graph_demo(page: Page):
    """Screenshot: Graph visualization demo page."""
    page.goto(f"{BASE}/visualize/demo")
    page.wait_for_timeout(8000)
    screenshot(page, "07_graph_demo")


def test_screenshot_08_graph_empty(page: Page):
    """Screenshot: Empty dataset graph view."""
    login(page)
    page.goto(f"{BASE}/visualize/nonexistent-id")
    page.wait_for_timeout(5000)
    screenshot(page, "08_graph_empty")


# ════════════════════════════════════════
# 5. Signup Page
# ════════════════════════════════════════

def test_screenshot_09_signup(page: Page):
    """Screenshot: Signup/registration page."""
    page.goto(f"{BASE}/auth/signup")
    page.wait_for_timeout(2000)
    screenshot(page, "09_signup")


# ════════════════════════════════════════
# 6. Health Endpoints
# ════════════════════════════════════════

def test_screenshot_10_health(page: Page):
    """Screenshot: Health JSON response."""
    page.goto(f"{BASE}/health")
    page.wait_for_timeout(1000)
    screenshot(page, "10_health")


def test_screenshot_11_health_details(page: Page):
    """Screenshot: Detailed health with all services."""
    page.goto(f"{BASE}/health/details")
    page.wait_for_timeout(1000)
    screenshot(page, "11_health_details")


# ════════════════════════════════════════
# 7. Error Pages
# ════════════════════════════════════════

def test_screenshot_12_404_page(page: Page):
    """Screenshot: 404 page for nonexistent route."""
    page.goto(f"{BASE}/nonexistent-page-xyz")
    page.wait_for_timeout(2000)
    screenshot(page, "12_404_page")


# ════════════════════════════════════════
# Summary
# ════════════════════════════════════════

def test_screenshot_summary(page: Page):
    """List all screenshots taken."""
    files = sorted(os.listdir(SCREENSHOTS_DIR))
    print(f"\n📸 Total screenshots: {len(files)}")
    for f in files:
        size = os.path.getsize(os.path.join(SCREENSHOTS_DIR, f))
        print(f"  {f} ({size // 1024}KB)")
