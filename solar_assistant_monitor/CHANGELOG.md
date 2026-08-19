# Changelog

## 0.1.0

- Detect frozen battery telemetry through the direct Solar Assistant Phoenix WebSocket.
- Require a changing LOAD/GRID witness before declaring a battery communication failure.
- Perform one bounded Disconnect/Connect attempt followed by at most one fingerprint-pinned SSH reboot.
- Persist incident state and queue optional MQTT output while the Home Assistant broker is unavailable.
- Publish MQTT Discovery entities and structured incident events.
