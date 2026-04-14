FROM golang:1.22-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY hls.min.js ./
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /tltv .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /tltv /tltv
EXPOSE 8000
WORKDIR /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/tltv", "version"]
ENTRYPOINT ["/tltv"]
