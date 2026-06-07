# Canary Rollout Guide

## Release Steps

Deploy the new service version to the canary slice first. Keep the feature flag
disabled until latency, error rate, and rollback checks pass.

## Rollback

If the canary shows elevated errors, disable the flag and return traffic to the
previous stable deployment.
