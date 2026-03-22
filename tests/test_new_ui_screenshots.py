"""
Скриншоты новых UI компонентов — Search, Ontologies, Collections,
Share modal, MCP anchors, StatusIndicator, Header nav.
"""
import os
import requests
import pytest
from playwright.sync_api import Page

BASE = "http://localhost:3000"
API = "http://localhost:8080/api/v1"
DIR = "/tmp/cognevra_screenshots_new"
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


# ═══════════ P6: SEARCH / CHAT ═══════════

def test_search_01_empty(page: Page):
    """Пустая страница /search до ввода запроса."""
    login(page)
    page.goto(f"{BASE}/search")
    page.wait_for_timeout(2000)
    shot(page, "search_01_empty")


def test_search_02_with_query(page: Page):
    """Вводим 'database', отправляем, ждём результат."""
    login(page)
    page.goto(f"{BASE}/search")
    page.wait_for_timeout(2000)
    # Ищем поле ввода и кнопку отправки
    search_input = page.locator('input[type="text"], input[type="search"], textarea').first
    search_input.fill("database")
    page.wait_for_timeout(500)
    # Отправка — кликаем кнопку или жмём Enter
    submit = page.locator('button[type="submit"], button:has-text("Search"), button:has-text("Send")')
    if submit.count() > 0:
        submit.first.click()
    else:
        search_input.press("Enter")
    page.wait_for_timeout(5000)
    shot(page, "search_02_with_query")


def test_search_03_type_selector(page: Page):
    """Переключаем тип поиска на RAG Completion."""
    login(page)
    page.goto(f"{BASE}/search")
    page.wait_for_timeout(2000)
    # Ищем селектор типа поиска
    selector = page.locator('select, [role="combobox"], [class*="select"], [class*="dropdown"]').first
    if selector.count() > 0:
        selector.click()
        page.wait_for_timeout(500)
        # Выбираем RAG Completion
        option = page.locator('text=RAG Completion, text=rag_completion, option:has-text("RAG")')
        if option.count() > 0:
            option.first.click()
        page.wait_for_timeout(1000)
    shot(page, "search_03_type_selector")


# ═══════════ P4: ONTOLOGIES ═══════════

def test_onto_04_empty(page: Page):
    """Пустая страница /ontologies."""
    login(page)
    page.goto(f"{BASE}/ontologies")
    page.wait_for_timeout(3000)
    shot(page, "onto_04_empty")


def test_onto_05_upload_area(page: Page):
    """Фокус на upload форму для загрузки онтологий."""
    login(page)
    page.goto(f"{BASE}/ontologies")
    page.wait_for_timeout(3000)
    # Скроллим к форме загрузки если она ниже
    upload = page.locator('[class*="upload"], [class*="dropzone"], input[type="file"], [class*="Upload"]')
    if upload.count() > 0:
        upload.first.scroll_into_view_if_needed()
        page.wait_for_timeout(500)
    shot(page, "onto_05_upload_area")


# ═══════════ P5: COLLECTIONS ═══════════

def test_coll_06_list(page: Page):
    """Страница /collections со списком коллекций."""
    login(page)
    page.goto(f"{BASE}/collections")
    page.wait_for_timeout(3000)
    shot(page, "coll_06_list")


def test_coll_07_stats(page: Page):
    """Фокус на stats bar — количество коллекций, записей."""
    login(page)
    page.goto(f"{BASE}/collections")
    page.wait_for_timeout(3000)
    # Скроллим к статистике
    stats = page.locator('[class*="stat"], [class*="Stats"], [class*="summary"], [class*="count"]')
    if stats.count() > 0:
        stats.first.scroll_into_view_if_needed()
        page.wait_for_timeout(500)
    shot(page, "coll_07_stats")


def test_coll_08_create_form(page: Page):
    """Фокус на форму создания новой коллекции."""
    login(page)
    page.goto(f"{BASE}/collections")
    page.wait_for_timeout(3000)
    # Ищем кнопку создания
    create_btn = page.locator('button:has-text("Create"), button:has-text("New"), button:has-text("Add"), [class*="create"]')
    if create_btn.count() > 0:
        create_btn.first.click()
        page.wait_for_timeout(1500)
    shot(page, "coll_08_create_form")


# ═══════════ P3: SHARE MODAL ═══════════

def test_share_09_popup_menu(page: Page):
    """Открываем popup menu датасета, видна опция 'share'."""
    login(page)
    page.wait_for_timeout(2000)
    # Ищем три-точки / кебаб-меню у датасета
    menu_btn = page.locator('[class*="menu"], [class*="kebab"], [class*="more"], [aria-label*="menu"], button:has-text("⋮"), button:has-text("...")')
    if menu_btn.count() > 0:
        menu_btn.first.click()
        page.wait_for_timeout(1000)
    shot(page, "share_09_popup_menu")


def test_share_10_modal_open(page: Page):
    """Открываем share modal через popup menu."""
    login(page)
    page.wait_for_timeout(2000)
    # Открываем popup menu
    menu_btn = page.locator('[class*="menu"], [class*="kebab"], [class*="more"], [aria-label*="menu"], button:has-text("⋮"), button:has-text("...")')
    if menu_btn.count() > 0:
        menu_btn.first.click()
        page.wait_for_timeout(1000)
        # Кликаем Share
        share_opt = page.locator('text=Share, text=share, [class*="share"]')
        if share_opt.count() > 0:
            share_opt.first.click()
            page.wait_for_timeout(1500)
    shot(page, "share_10_modal_open")


# ═══════════ P8: MCP STATUS С ЯКОРЯМИ ═══════════

def test_mcp_11_quick_nav(page: Page):
    """Верхняя часть /mcp-status с quick-nav links."""
    login(page)
    page.goto(f"{BASE}/mcp-status")
    page.wait_for_timeout(5000)
    shot(page, "mcp_11_quick_nav")


def test_mcp_12_tools_anchor(page: Page):
    """Переход по якорю /mcp-status#tools — скролл до tools section."""
    login(page)
    page.goto(f"{BASE}/mcp-status#tools")
    page.wait_for_timeout(5000)
    # Дополнительный скролл к секции tools если якорь не сработал
    tools_section = page.locator('#tools, [id="tools"], h2:has-text("Tools"), h3:has-text("Tools")')
    if tools_section.count() > 0:
        tools_section.first.scroll_into_view_if_needed()
        page.wait_for_timeout(1000)
    shot(page, "mcp_12_tools_anchor")


# ═══════════ P7: STATUS INDICATOR ═══════════

def test_status_13_dashboard(page: Page):
    """Dashboard с datasets и StatusIndicator компонентом."""
    login(page)
    page.wait_for_timeout(3000)
    # Скроллим к области датасетов где виден StatusIndicator
    page.evaluate("window.scrollTo(0, 300)")
    page.wait_for_timeout(1000)
    shot(page, "status_13_dashboard")


# ═══════════ HEADER NAVIGATION ═══════════

def test_header_14_nav_links(page: Page):
    """Header с новыми ссылками — Search, Ontologies, Collections."""
    login(page)
    page.wait_for_timeout(2000)
    # Скриншот верхней части с header
    shot(page, "header_14_nav_links")


# ═══════════ ИТОГ ═══════════

def test_summary_new_screenshots(page: Page):
    """Выводим список всех сделанных скриншотов."""
    files = sorted(os.listdir(DIR))
    print(f"\n{'='*60}")
    print(f"📸 ВСЕГО СКРИНШОТОВ НОВЫХ КОМПОНЕНТОВ: {len(files)}")
    print(f"{'='*60}")
    for f in files:
        size = os.path.getsize(os.path.join(DIR, f))
        print(f"  {f:45s} {size//1024:>4d}KB")
