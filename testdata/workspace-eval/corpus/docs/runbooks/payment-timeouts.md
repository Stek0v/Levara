# Payment Service Runbook

## Symptoms

Checkout requests can fail when the payment service waits too long for an
upstream bank response. The customer-facing error often appears as a bounded
timeout during authorization.

## Mitigation

Use the retry budget, shorten the gateway deadline, and keep idempotency keys
for every payment attempt. The rollback anchor for this runbook is
`payment-timeout-mitigation`.

## Prior Learning

The May incident showed that slow deploys combined with payment authorization
latency make timeout errors look like general checkout failures.
