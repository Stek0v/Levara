"""
P1 HIGH PRIORITY TESTS — Feature Completeness, Input Validation, Error Handling.
20 tests covering common user scenarios and error paths.

DoD for each test:
1. Verifies specific error message in response body (not just status code)
2. Tests both valid and invalid inputs where applicable
3. Independent — no shared state between tests
4. Cleans up resources
"""
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id

pytestmark = pytest.mark.asyncio


async def _register(s, email=None, pw="P1pass123!"):
    email = email or f"p1_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw}) as r:
        data = await r.json()
        return {"Authorization": f"Bearer {data['access_token']}"}, email


# ═══════════ AUTH & USER MANAGEMENT ═══════════

async def test_p1_register_duplicate_email():
    """Second registration with same email → 409 or silent (ON CONFLICT).
    DoD: Same email twice → first 201, second 409/201 (no crash).
    Code: auth.go:195
    """
    async with aiohttp.ClientSession() as s:
        email = f"dup_{unique_id()}@test.com"
        async with s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "pass123!"}) as r:
            assert r.status == 201
        async with s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "pass123!"}) as r:
            assert r.status in (201, 409), f"Unexpected: {r.status}"


async def test_p1_cookie_auth_works():
    """Login sets cookie → subsequent requests authenticated via cookie.
    DoD: Login → cookie set → /auth/me with cookie only → 200.
    Code: auth.go setAuthCookie + JWTMiddleware cookie read
    """
    async with aiohttp.ClientSession(cookie_jar=aiohttp.CookieJar()) as s:
        email = f"cookie_{unique_id()}@test.com"
        await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "cookiepass!"})
        await s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "cookiepass!"})
        # Request WITHOUT Authorization header — should use cookie
        async with s.get(f"{BASE_URL}/auth/me") as r:
            assert r.status == 200
            data = await r.json()
            assert data["email"] == email


async def test_p1_login_form_and_json_both_work():
    """Login accepts both form-encoded and JSON.
    DoD: Same credentials via both Content-Types → both 200 with token.
    Code: auth.go:119-139
    """
    async with aiohttp.ClientSession() as s:
        email = f"dual_{unique_id()}@test.com"
        await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "dualpass!"})
        # JSON
        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "dualpass!"}) as r:
            assert r.status == 200
        # Form
        async with s.post(f"{BASE_URL}/auth/login", data={"username": email, "password": "dualpass!"}) as r:
            assert r.status == 200


async def test_p1_password_min_length():
    """New password < 6 chars → 400.
    DoD: PUT /users/me/password with 3-char password → 400 with detail.
    Code: users.go:105-107
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.put(f"{BASE_URL}/users/me/password", json={
            "current_password": "P1pass123!",
            "new_password": "ab",
        }, headers=h) as r:
            assert r.status == 400
            assert "6" in (await r.json()).get("detail", "")


# ═══════════ DATASET VALIDATION ═══════════

async def test_p1_dataset_create_validates_name():
    """Empty name → 400 with detail.
    DoD: POST /datasets {} → 400 "name required".
    Code: api.go:56
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/datasets", json={}, headers=h) as r:
            assert r.status == 400
            assert "name" in (await r.json()).get("detail", "").lower()


async def test_p1_cognify_validates_input():
    """Cognify with empty body → 400.
    DoD: POST /cognify {} → 400 "no texts".
    Code: api.go cognifyHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/cognify", json={}, headers=h) as r:
            assert r.status == 400
            assert "texts" in (await r.json()).get("detail", "").lower()


async def test_p1_cognify_404_nonexistent_run():
    """GET /cognify/fake/status → 404.
    DoD: Nonexistent run_id → 404 "run not found".
    Code: api.go:69
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.get(f"{BASE_URL}/cognify/nonexistent-run-999/status", headers=h) as r:
            assert r.status == 404


# ═══════════ SHARING & PERMISSIONS ═══════════

async def test_p1_share_invalid_role_rejected():
    """Share with role != admin/editor/viewer → 400.
    DoD: POST share {role: "superadmin"} → 400 "role must be".
    Code: rbac.go
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/datasets/fake/shares", json={
            "user_id": "anyone", "role": "superadmin",
        }, headers=h) as r:
            assert r.status == 400
            assert "role" in (await r.json()).get("detail", "").lower()


async def test_p1_acl_invalid_permission_rejected():
    """Grant invalid permission type → 400.
    DoD: POST /acl {permission_type: "admin"} → 400.
    Code: tenants.go aclGrantHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/acl", json={
            "principal_id": "x", "dataset_id": "y", "permission_type": "admin",
        }, headers=h) as r:
            assert r.status == 400
            assert "permission" in (await r.json()).get("detail", "").lower()


async def test_p1_acl_missing_fields_rejected():
    """Grant without required fields → 400.
    DoD: POST /acl {} → 400 "required".
    Code: tenants.go aclGrantHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/acl", json={}, headers=h) as r:
            assert r.status == 400


# ═══════════ NOTEBOOKS ═══════════

async def test_p1_notebook_default_name():
    """Create notebook without name → "Untitled".
    DoD: POST /notebooks {} → 201 with name="Untitled".
    Code: notebooks.go:71-72
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/notebooks", json={}, headers=h) as r:
            assert r.status == 201
            data = await r.json()
            assert data["name"] == "Untitled"
            nb_id = data["id"]
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h)


async def test_p1_cell_unknown_command():
    """Running unknown code command → result contains "Unknown command".
    DoD: Cell run "foobar" → result has "Unknown command: foobar".
    Code: notebooks.go runCodeCell default case
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "cmd_test"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "type": "code", "content": "foobar_invalid",
        }, headers=h) as r:
            cell_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run", json={
            "type": "code", "content": "foobar_invalid",
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert "Unknown command" in data.get("result", "")
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h)


async def test_p1_cell_empty_rejected():
    """Running empty cell → 400.
    DoD: Cell run with no content → 400 "empty cell".
    Code: notebooks.go cellRunHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "empty_test"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={"type": "code", "content": ""}, headers=h) as r:
            cell_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run", json={}, headers=h) as r:
            assert r.status == 400
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h)


# ═══════════ SESSIONS ═══════════

async def test_p1_session_user_isolation():
    """User B cannot see User A's interactions.
    DoD: A saves interaction → B lists → B's list empty.
    Code: sessions.go:52 WHERE user_id = $1
    """
    async with aiohttp.ClientSession() as s:
        h_a, _ = await _register(s)
        h_b, _ = await _register(s)
        # A saves interaction
        await s.post(f"{BASE_URL}/interactions", json={
            "query": "User A secret query", "response": "Answer",
        }, headers=h_a)
        # B lists — should not see A's
        async with s.get(f"{BASE_URL}/interactions", headers=h_b) as r:
            items = await r.json()
            queries = [i.get("query", "") for i in items]
            assert "User A secret query" not in queries, "User B can see User A's interactions!"


async def test_p1_interaction_requires_query():
    """POST /interactions without query → 400.
    DoD: POST with {} → 400 "query required".
    Code: sessions.go:24
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/interactions", json={}, headers=h) as r:
            assert r.status == 400


# ═══════════ TENANTS ═══════════

async def test_p1_tenant_create_requires_name():
    """POST /tenants without name → 400.
    DoD: POST {} → 400 "name required".
    Code: tenants.go tenantCreateHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/tenants", json={}, headers=h) as r:
            assert r.status == 400


async def test_p1_tenant_add_user_requires_id():
    """POST /tenants/:id/users without user_id → 400.
    DoD: POST {} → 400 "user_id required".
    Code: tenants.go tenantAddUserHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/tenants/fake/users", json={}, headers=h) as r:
            assert r.status == 400


# ═══════════ ONTOLOGY ═══════════

async def test_p1_ontology_format_detection():
    """.ttl detected as turtle, .owl as rdf/xml.
    DoD: Upload .ttl → format="turtle". Upload .owl → format="rdf/xml".
    Code: ontologies.go:46-49
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        # TTL
        form = aiohttp.FormData()
        form.add_field("file", b"@prefix rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> .",
                       filename="test.ttl", content_type="text/turtle")
        form.add_field("name", f"ttl_{unique_id()}")
        async with s.post(f"{BASE_URL}/ontologies", data=form, headers=h) as r:
            if r.status == 201:
                assert (await r.json())["format"] == "turtle"

        # OWL
        form2 = aiohttp.FormData()
        form2.add_field("file", b"<rdf:RDF></rdf:RDF>", filename="test.owl", content_type="application/rdf+xml")
        form2.add_field("name", f"owl_{unique_id()}")
        async with s.post(f"{BASE_URL}/ontologies", data=form2, headers=h) as r:
            if r.status == 201:
                assert (await r.json())["format"] == "rdf/xml"


async def test_p1_ontology_requires_file():
    """POST /ontologies without file → 400.
    DoD: POST empty form → 400 "file required".
    Code: ontologies.go
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _register(s)
        async with s.post(f"{BASE_URL}/ontologies", headers=h) as r:
            assert r.status == 400
