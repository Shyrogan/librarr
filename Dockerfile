FROM golang:1.25-alpine AS builder

WORKDIR /build

# Cache dependencies.
COPY go.mod go.sum ./
RUN go mod download

# Build the binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /librarr ./cmd/librarr/

# --- Runtime image ---
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 librarr

COPY --from=builder /librarr /usr/local/bin/librarr

USER librarr
EXPOSE 5050

ENTRYPOINT ["/usr/local/bin/librarr"]
