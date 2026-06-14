FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY main.go .
# Initialize a module to manage dependencies
RUN go mod init matching-engine

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o engine .

FROM alpine:latest

WORKDIR /app
COPY --from=builder /app/engine .

EXPOSE 8080

CMD ["./engine"]
