"""
50 SCREENSHOTS — каждый критичный шаг каждого из 20 сценариев.
После прогона Claude анализирует каждый скриншот и составляет отчёт проблем.
"""
import os
import requests
import pytest
from playwright.sync_api import Page

BASE = "http://localhost:3000"
API = "http://localhost:8080/api/v1"
DIR = "/tmp/cognevra_screenshots_full"
EMAIL = "admin@cognevra.dev"
PASSWORD = "admin123456"

os.makedirs(DIR, exist_ok=True)


def shot(page: Page, name: str):
    path = os.path.join(DIR, f"{name}.png")
    page.screenshot(path=path, full_page=True)
    print(f"📸 {name}")
    return path


def login(page: Page):
    page.goto(f"{BASE}/auth/login")
    page.wait_for_timeout(1500)
    page.fill('input[name="email"]', EMAIL)
    page.fill('input[name="password"]', PASSWORD)
    page.click('button[type="submit"]')
    page.wait_for_timeout(3000)


# ═══════════ S01-S03: UPLOAD + SEARCH ═══════════

def test_s01_01_dashboard_before_upload(page: Page):
    login(page)
    shot(page, "s01_01_dashboard_initial")

def test_s01_02_add_data_area(page: Page):
    login(page)
    page.wait_for_timeout(1000)
    # Click "Add data to cognee" if visible
    add_btn = page.locator("text=Add data to cognee")
    if add_btn.count() > 0:
        add_btn.first.click()
        page.wait_for_timeout(1000)
    shot(page, "s01_02_add_data_area")

def test_s01_03_datasets_section(page: Page):
    login(page)
    page.wait_for_timeout(2000)
    # Scroll to Cognee Instances
    page.evaluate("window.scrollTo(0, 400)")
    page.wait_for_timeout(500)
    shot(page, "s01_03_datasets_section")

def test_s01_04_search_api_results(page: Page):
    """Screenshot API search results as JSON."""
    page.goto(f"{BASE}/api/v1/search/text")
    page.wait_for_timeout(1000)
    # POST via API and show results
    r = requests.post(f"{API}/search/text", json={"query_text": "database", "query_type": "CHUNKS", "top_k": 3}, timeout=5)
    page.goto(f"data:application/json,{r.text[:500]}")
    page.wait_for_timeout(500)
    shot(page, "s01_04_search_results")

# ═══════════ S04: SHARING ═══════════

def test_s04_05_user_a_datasets(page: Page):
    login(page)
    page.wait_for_timeout(2000)
    page.evaluate("window.scrollTo(0, 300)")
    shot(page, "s04_05_user_a_datasets")

# ═══════════ S05: TEMPORAL ═══════════

def test_s05_06_temporal_api(page: Page):
    """Temporal search results via API."""
    r = requests.post(f"{API}/search/text", json={
        "query_text": "events in January 2024", "query_type": "TEMPORAL"
    }, timeout=5)
    page.goto(f"data:text/plain,TEMPORAL SEARCH RESULTS: {r.text[:300]}")
    page.wait_for_timeout(500)
    shot(page, "s05_06_temporal_results")

# ═══════════ S06: NOTEBOOKS ═══════════

def test_s06_07_notebooks_list(page: Page):
    login(page)
    page.wait_for_timeout(2000)
    shot(page, "s06_07_notebooks_sidebar")

def test_s06_08_notebook_editor(page: Page):
    login(page)
    page.wait_for_timeout(2000)
    # Click first notebook
    nb = page.locator('[class*="notebook"], [class*="Notebook"]').first
    if nb.count() > 0:
        nb.click()
        page.wait_for_timeout(1000)
    shot(page, "s06_08_notebook_editor")

# ═══════════ S07: MCP ═══════════

def test_s07_09_mcp_status_top(page: Page):
    login(page)
    page.goto(f"{BASE}/mcp-status")
    page.wait_for_timeout(5000)
    shot(page, "s07_09_mcp_services")

def test_s07_10_mcp_tools_scroll(page: Page):
    login(page)
    page.goto(f"{BASE}/mcp-status")
    page.wait_for_timeout(5000)
    page.evaluate("window.scrollTo(0, 800)")
    page.wait_for_timeout(1000)
    shot(page, "s07_10_mcp_tools")

def test_s07_11_mcp_integration(page: Page):
    login(page)
    page.goto(f"{BASE}/mcp-status")
    page.wait_for_timeout(5000)
    page.evaluate("window.scrollTo(0, 1500)")
    page.wait_for_timeout(1000)
    shot(page, "s07_11_mcp_integration")

# ═══════════ S08: SETTINGS ═══════════

def test_s08_12_settings_api(page: Page):
    r = requests.get(f"{API}/settings", timeout=5)
    page.goto(f"data:application/json,{r.text}")
    page.wait_for_timeout(500)
    shot(page, "s08_12_settings_json")

# ═══════════ S13-S14: ONTOLOGY + COLLECTIONS ═══════════

def test_s13_13_ontology_list(page: Page):
    r = requests.get(f"{API}/ontologies", timeout=5)
    page.goto(f"data:application/json,{r.text}")
    page.wait_for_timeout(500)
    shot(page, "s13_13_ontology_list")

def test_s14_14_collections_list(page: Page):
    r = requests.get(f"{API}/collections", timeout=5)
    page.goto(f"data:application/json,{r.text[:800]}")
    page.wait_for_timeout(500)
    shot(page, "s14_14_collections")

# ═══════════ S15: DATASET LIFECYCLE ═══════════

def test_s15_15_datasets_list(page: Page):
    r = requests.get(f"{API}/datasets", timeout=5)
    page.goto(f"data:application/json,{r.text[:500]}")
    page.wait_for_timeout(500)
    shot(page, "s15_15_datasets_list")

# ═══════════ S17: AUTH ═══════════

def test_s17_16_login_empty(page: Page):
    page.goto(f"{BASE}/auth/login")
    page.wait_for_timeout(2000)
    shot(page, "s17_16_login_empty")

def test_s17_17_login_filled(page: Page):
    page.goto(f"{BASE}/auth/login")
    page.wait_for_timeout(1500)
    page.fill('input[name="email"]', EMAIL)
    page.fill('input[name="password"]', PASSWORD)
    shot(page, "s17_17_login_filled")

def test_s17_18_signup(page: Page):
    page.goto(f"{BASE}/auth/signup")
    page.wait_for_timeout(2000)
    shot(page, "s17_18_signup")

def test_s17_19_dashboard_after_login(page: Page):
    login(page)
    shot(page, "s17_19_dashboard_authed")

def test_s17_20_account_page(page: Page):
    login(page)
    page.goto(f"{BASE}/account")
    page.wait_for_timeout(2000)
    shot(page, "s17_20_account")

# ═══════════ S18-S19: GRAPH + HEALTH ═══════════

def test_s18_21_graph_demo(page: Page):
    page.goto(f"{BASE}/visualize/demo")
    page.wait_for_timeout(8000)
    shot(page, "s18_21_graph_demo")

def test_s18_22_graph_demo_zoomed(page: Page):
    page.goto(f"{BASE}/visualize/demo")
    page.wait_for_timeout(8000)
    # Simulate zoom
    page.mouse.wheel(0, -300)
    page.wait_for_timeout(2000)
    shot(page, "s18_22_graph_zoomed")

def test_s18_23_graph_real_dataset(page: Page):
    login(page)
    # Get first dataset ID
    r = requests.get(f"{API}/datasets", timeout=5)
    datasets = r.json()
    if datasets:
        ds_id = datasets[0]["id"]
        page.goto(f"{BASE}/visualize/{ds_id}")
        page.wait_for_timeout(5000)
    else:
        page.goto(f"{BASE}/visualize/no-data")
        page.wait_for_timeout(3000)
    shot(page, "s18_23_graph_dataset")

def test_s19_24_health_json(page: Page):
    page.goto(f"{BASE}/health")
    page.wait_for_timeout(1000)
    shot(page, "s19_24_health")

def test_s19_25_health_details(page: Page):
    page.goto(f"{BASE}/health/details")
    page.wait_for_timeout(1000)
    shot(page, "s19_25_health_details")

# ═══════════ S20: STRESS + EDGE ═══════════

def test_s20_26_after_rapid_load(page: Page):
    login(page)
    for _ in range(3):
        page.goto(f"{BASE}/")
        page.wait_for_timeout(300)
    shot(page, "s20_26_after_rapid")

def test_edge_27_404(page: Page):
    page.goto(f"{BASE}/nonexistent-xyz")
    page.wait_for_timeout(1500)
    shot(page, "edge_27_404")

def test_edge_28_plan_page(page: Page):
    login(page)
    page.goto(f"{BASE}/plan")
    page.wait_for_timeout(2000)
    shot(page, "edge_28_plan")

def test_edge_29_empty_graph(page: Page):
    login(page)
    page.goto(f"{BASE}/visualize/empty-dataset-no-data")
    page.wait_for_timeout(5000)
    shot(page, "edge_29_graph_empty")

# ═══════════ SUMMARY ═══════════

def test_summary_all_screenshots(page: Page):
    files = sorted(os.listdir(DIR))
    print(f"\n{'='*60}")
    print(f"📸 TOTAL SCREENSHOTS: {len(files)}")
    print(f"{'='*60}")
    for f in files:
        size = os.path.getsize(os.path.join(DIR, f))
        print(f"  {f:45s} {size//1024:>4d}KB")
