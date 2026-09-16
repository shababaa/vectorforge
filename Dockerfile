FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/marketserver ./cmd/marketserver

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && addgroup -S marketdb \
    && adduser -S -G marketdb -h /data marketdb
WORKDIR /data
COPY --from=build /out/marketserver /usr/local/bin/marketserver
USER marketdb
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["marketserver"]
CMD ["-addr", "0.0.0.0:8080", "-db", "/data/market.db", "-seed-demo"]
