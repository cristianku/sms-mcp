# Optional container build. Static binary on distroless (no shell, nonroot).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/sms-mcp ./cmd/sms-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sms-mcp /usr/local/bin/sms-mcp
WORKDIR /data
ENV SMS_MCP_SESSION_FILE=/data/session.json \
    SMS_MCP_AUDIT_FILE=/data/audit.jsonl \
    SMS_MCP_QR_PNG=/data/pairing-qr.png
# Inside a container the listener must be reachable from outside, so a token
# is required (SMS_MCP_TOKEN or SMS_MCP_TOKEN_FILE). Publish the port on
# 127.0.0.1 only, e.g. -p 127.0.0.1:8787:8787.
ENV SMS_MCP_LISTEN=0.0.0.0:8787
EXPOSE 8787
VOLUME ["/data"]
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sms-mcp"]
CMD ["serve"]
