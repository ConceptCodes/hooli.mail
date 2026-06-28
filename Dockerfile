FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /build/bin && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /build/bin/server ./cmd/server

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

RUN adduser -D -h /app hooli
USER hooli
WORKDIR /app

COPY --from=builder /build/bin/server .

EXPOSE 80 143 587 993 2525

ENV DOMAIN=""
ENV DSN="postgres://hooli:hooli@localhost:5432/hoolimail?sslmode=disable"
ENV SMTP_PORT="2525"
ENV SUBMISSION_PORT="587"
ENV IMAP_PORT="143"
ENV IMAPS_PORT="993"

ENTRYPOINT ["/app/server"]
