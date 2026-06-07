# Auth Token Rotation

## Rotation Policy

JWT signing keys rotate every seven days. Services must refresh the JWKS cache
before old tokens expire.

## Incident Playbook

If login sessions fail after a key rotation, invalidate stale session cookies
and verify the current key id in the token header.
