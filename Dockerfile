# syntax=docker/dockerfile:1

FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist ./web/dist
ARG VERSION=0.2.0
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X xiaoyurepeater/internal/version.Version=${VERSION}" \
    -o /out/xiaoyurepeater .

FROM alpine:3.22
RUN adduser -D -H -u 10001 app \
    && mkdir -p /data \
    && chown app:app /data
COPY --from=build /out/xiaoyurepeater /usr/local/bin/xiaoyurepeater
USER app
ENV XR_ADDR=:8080 \
    XR_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080
EXPOSE 50000-50100/udp
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["xiaoyurepeater"]
