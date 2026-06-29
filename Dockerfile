# PIN: replace sha256 placeholders with current digests via:
#   docker pull golang:1.25-alpine && docker image inspect --format '{{index .RepoDigests 0}}' golang:1.25-alpine
FROM golang:1.25-alpine@sha256:PLACEHOLDER_GOLANG_DIGEST AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /build/bin && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /build/bin/server ./cmd/server

# PIN: replace sha256 placeholders with current digests via:
#   docker pull alpine:3.21 && docker image inspect --format '{{index .RepoDigests 0}}' alpine:3.21
FROM alpine:3.21@sha256:PLACEHOLDER_ALPINE_DIGEST

RUN apk add --no-cache ca-certificates tzdata

RUN adduser -D -h /app hooli
USER hooli
WORKDIR /app

COPY --from=builder --chown=hooli:hooli /build/bin/server .

# Port 80 is used solely for ACME HTTP-01 challenges; no credentials or
# cookies are served over plaintext HTTP.
EXPOSE 80 143 587 993 2525

LABEL org.opencontainers.image.source="https://github.com/anomalyco/mail-server" \
      org.opencontainers.image.title="hoolimail-server" \
      org.opencontainers.image.licenses="MIT"

ARG GIT_COMMIT=unknown
LABEL org.opencontainers.image.revision="${GIT_COMMIT}"

ENV DOMAIN=""
ENV DSN="${DSN:?Set DSN to your Postgres connection string}"
ENV SMTP_PORT="2525"
ENV SUBMISSION_PORT="587"
ENV IMAP_PORT="143"
ENV IMAPS_PORT="993"

ENTRYPOINT ["/app/server"]
