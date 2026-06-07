# Search Latency Incident

## Timeline

Search requests slowed down after the ranking service exhausted its worker
pool. The incident was unrelated to payment timeout handling.

## Fix

Increase queue capacity, add ranking service saturation alerts, and document
the rollback command for search traffic.
