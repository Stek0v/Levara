# Base host systemd configuration

These files preserve the Qwythos configuration proven stable on the shared
RTX 3090 and keep Levara running when an optional model service fails.

The Levara unit is a full replacement because systemd dependency directives
cannot be cleared in a drop-in. It changes the existing model dependencies
from `Requires=` to `Wants=` and retains their `After=` ordering.

## Install

Run from the repository root on `base`:

```sh
stamp=$(date -u +%Y%m%dT%H%M%SZ)
echo "Rollback timestamp: $stamp"
sudo cp -a /etc/systemd/system/levara.service "/etc/systemd/system/levara.service.bak.$stamp"
if test -f /etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf; then
  sudo cp -a /etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf "/etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf.bak.$stamp"
fi
sudo install -m 0644 deploy/base/levara.service /etc/systemd/system/levara.service
sudo install -D -m 0644 deploy/base/qwythos-levara.service.d/zzzz-levara-stable.conf /etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf
sudo systemctl daemon-reload
```

`zzzz-levara-stable.conf` sorts after the legacy `zzz-qwen3-64k.conf` and
resets `ExecStart` before selecting the 32768-token configuration. Do not
restore 64K context until the complete co-resident GPU memory budget has been
measured and verified under load.

## Verify

```sh
sudo systemd-analyze verify /etc/systemd/system/levara.service /etc/systemd/system/qwythos-levara.service
systemctl show levara.service -p After -p Wants -p Requires
systemctl show qwythos-levara.service -p ExecStart
```

The model units must appear in `Wants` and `After`, not in `Requires`.
Qwythos `ExecStart` must contain `--ctx-size 32768`.

## Controlled reconcile

Reconcile Qwythos first, then touch Levara only after the model is healthy:

```sh
sudo systemctl restart qwythos-levara.service
curl --fail --retry 30 --retry-delay 2 http://127.0.0.1:11435/health
sudo systemctl try-restart levara.service
systemctl --no-pager --full status qwythos-levara.service levara.service
```

If an optional model remains unavailable, leave it failed and verify that
Levara stays active:

```sh
systemctl is-active levara.service
curl --fail http://127.0.0.1:8080/health
```

## Roll back

Restore the backups created during installation, reload systemd, and restart
the affected units:

```sh
stamp=20261008T000000Z # replace with the timestamp printed during installation
sudo cp -a "/etc/systemd/system/levara.service.bak.$stamp" /etc/systemd/system/levara.service
if test -f "/etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf.bak.$stamp"; then
  sudo cp -a "/etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf.bak.$stamp" /etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf
else
  sudo rm -f /etc/systemd/system/qwythos-levara.service.d/zzzz-levara-stable.conf
fi
sudo systemctl daemon-reload
sudo systemctl restart qwythos-levara.service levara.service
```
