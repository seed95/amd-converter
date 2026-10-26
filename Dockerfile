# ---- build ----
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY static ./static
RUN go vet ./... && go test ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/amd-converter .

# ---- run ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app
USER app
COPY --from=build /out/amd-converter /usr/local/bin/amd-converter
ENV PORT=8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:${PORT}/healthz >/dev/null || exit 1
ENTRYPOINT ["amd-converter"]
