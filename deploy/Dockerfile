FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sentinel ./cmd/sentinel

FROM alpine:3.23
RUN apk add --no-cache ca-certificates ffmpeg && addgroup -S sentinel && adduser -S -G sentinel -u 10001 sentinel
COPY --from=build /sentinel /usr/local/bin/sentinel
WORKDIR /app
USER sentinel
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["sentinel", "healthcheck"]
ENTRYPOINT ["sentinel"]
CMD ["run", "--config", "/app/configs/config.yaml"]
