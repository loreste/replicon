FROM golang:1.26.1 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/replicon .

FROM gcr.io/distroless/static-debian12

WORKDIR /app

COPY --from=builder /out/replicon /usr/local/bin/replicon
COPY docs /app/docs
COPY integration /app/integration

EXPOSE 8443

USER nonroot:nonroot

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/replicon", "help"]

ENTRYPOINT ["/usr/local/bin/replicon"]
