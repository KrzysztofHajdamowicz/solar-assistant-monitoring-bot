# Solar Assistant Monitoring Bot

A Home Assistant OS app that detects silent Solar Assistant RS485 battery telemetry stalls through the device's direct WebSocket and performs a bounded recovery ladder.

## Why direct WebSocket

Detection connects directly to Solar Assistant and therefore has no dependency on an MQTT bridge. A failure requires all configured batteries to stop changing while LOAD or GRID continues to change. MQTT is used only as optional Home Assistant output.

## Installation

1. Add this repository URL to the Home Assistant app store:

   ```text
   https://github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot
   ```

2. Install **Solar Assistant Monitoring Bot**.
3. Configure the local Solar Assistant password, stable SSH address, and verified SSH host-key fingerprint.
4. Start the app and inspect its logs and MQTT entities.

Detailed configuration and behavior are documented in [solar_assistant_monitor/DOCS.md](solar_assistant_monitor/DOCS.md).

## Notification blueprint

Import the blueprint from:

```text
https://github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot/raw/main/blueprints/automation/solar_assistant_monitor/notify.yaml
```

Select one or more notification actions. The `event` variable contains the structured monitor event and can be used in notification templates.

## Development

```sh
cd solar_assistant_monitor
go test -race ./...
go vet ./...
docker build --build-arg BUILD_ARCH=aarch64 --build-arg BUILD_VERSION=0.1.0 .
```

The production device is never disconnected or rebooted by automated repository tests.

## License

MIT
