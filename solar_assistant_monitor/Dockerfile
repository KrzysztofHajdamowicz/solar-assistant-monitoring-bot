FROM golang:1.27-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/solar-assistant-monitor ./cmd/monitor

FROM builder AS test
RUN apk add --no-cache chromium
ENV RUN_BROWSER_TESTS=1
ENV CHROMIUM_PATH=/usr/bin/chromium-browser
RUN go test ./...

FROM ghcr.io/home-assistant/base@sha256:94ff231402a5e7ad2a82e261ad5fa4ffae7d7bb095c3febb2edbdf309c9b6aca

ARG BUILD_ARCH
ARG BUILD_VERSION=0.1.0

RUN apk add --no-cache chromium

COPY --from=builder /out/solar-assistant-monitor /usr/bin/solar-assistant-monitor
COPY run.sh /run.sh
RUN chmod 0755 /run.sh /usr/bin/solar-assistant-monitor

LABEL \
  io.hass.name="Solar Assistant Monitoring Bot" \
  io.hass.description="Detects stalled battery telemetry and performs bounded recovery" \
  io.hass.type="app" \
  io.hass.arch="${BUILD_ARCH}" \
  io.hass.version="${BUILD_VERSION}" \
  org.opencontainers.image.source="https://github.com/KrzysztofHajdamowicz/solar-assistant-monitoring-bot" \
  org.opencontainers.image.licenses="MIT"

CMD ["/run.sh"]
