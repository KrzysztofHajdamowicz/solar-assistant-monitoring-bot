# Solar Assistant Monitoring Bot

This app monitors Solar Assistant directly. It does not use MQTT input for failure detection.

## Configuration

1. Set `solar_assistant_url` to the local URL of the device.
2. Enter the Solar Assistant local password. The same password is used by Solar Assistant for the `solar-assistant` SSH user.
3. Set `ssh_host` to the stable local address and port, for example `192.168.1.96:22`.
4. Verify and enter the SSH host key fingerprint. Never copy a fingerprint from an untrusted network response without comparing it on the device or another trusted channel.
5. Leave `automatic_recovery` disabled during an observation-only commissioning period if you do not want immediate automated actions.

The current device fingerprint used during development was:

```text
SHA256:07rnJVa1+hNk3EVTkVhD3mPNGOTlFTuRq08gJys1QXo
```

Obtain the currently presented fingerprint with:

```sh
ssh-keyscan -t ed25519 solar-assistant.local 2>/dev/null | ssh-keygen -lf -
```

## Detection

The app subscribes to dynamic `battery_N/*` metrics and to `total/load_power` plus `total/grid_power` over Solar Assistant's direct Phoenix WebSocket. Repeated values are not treated as activity. A full failure requires every configured battery to remain unchanged for `freeze_seconds` while LOAD or GRID changes within `witness_seconds`.

A single stale battery produces a warning but does not trigger recovery. A disconnected WebSocket or an inactive LOAD/GRID witness produces `monitor_error` and also does not trigger recovery.

## Recovery ladder

1. Confirm the condition for another 10 seconds.
2. Save a REST metrics snapshot in the incident record for diagnosis. REST data never decides whether recovery should run.
3. Use headless Chromium to click **Disconnect**, wait, click **Connect**, and require both UI statuses to become `Connected`.
4. Require every battery to produce changed telemetry.
5. If reconnect fails, use fingerprint-pinned SSH to run one `sudo reboot`.
6. Require a new WebSocket session, then re-check UI status and changed battery telemetry after boot.

Only one reconnect and one reboot are allowed per incident. `/data/state.json` survives app restarts and prevents reboot loops. The incident is rearmed only after the configured healthy period.

## MQTT output

MQTT is optional output only. When a Home Assistant MQTT service exists, the app publishes:

- `solar_assistant_monitor/availability`
- `solar_assistant_monitor/state`
- `solar_assistant_monitor/event`

The service is declared as `mqtt:want`; broker downtime does not stop monitoring or recovery. Events remain queued in `/data/state.json` and are flushed after reconnect. Home Assistant MQTT Discovery creates a problem binary sensor and reaction sensors.

The notification blueprint in this repository listens to the event topic and runs user-selected Home Assistant actions.

## Severity

| Level | Meaning |
|---:|---|
| 1 | Detected or recovered before an action |
| 2 | Disconnect/Connect attempted |
| 3 | Device reboot attempted |
| 4 | Automatic recovery exhausted |

## Security

Passwords are read from app options and are never included in logs, state payloads, or MQTT. Reboot is refused when the SSH fingerprint does not match exactly.
