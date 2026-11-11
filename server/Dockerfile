FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /easy-upload ./cmd/easy_upload

FROM alpine:3.21
RUN addgroup -S app && adduser -S app -G app && mkdir -p /data && chown app:app /data
USER app
COPY --from=builder /easy-upload /easy-upload
ENTRYPOINT ["/easy-upload"]
