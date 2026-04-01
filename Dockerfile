FROM golang:1.22-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /tltv .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /tltv /tltv
EXPOSE 8000
WORKDIR /data
ENTRYPOINT ["/tltv"]
