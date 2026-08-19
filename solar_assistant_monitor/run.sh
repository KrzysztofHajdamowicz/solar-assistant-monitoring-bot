#!/usr/bin/with-contenv bashio
set -euo pipefail

export OPTIONS_PATH=/data/options.json
export STATE_PATH=/data/state.json
export CHROMIUM_PATH=/usr/bin/chromium-browser

if bashio::services.available mqtt; then
  export MQTT_HOST="$(bashio::services mqtt host)"
  export MQTT_PORT="$(bashio::services mqtt port)"
  export MQTT_USERNAME="$(bashio::services mqtt username)"
  export MQTT_PASSWORD="$(bashio::services mqtt password)"
  export MQTT_SSL="$(bashio::services mqtt ssl)"
else
  bashio::log.warning "Home Assistant MQTT service is unavailable; monitoring and recovery continue without MQTT output"
fi

exec /usr/bin/solar-assistant-monitor
