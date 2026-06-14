FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod ./
# Copy go.sum if it exists, otherwise ignore error
COPY go.sum* ./

RUN go mod download

COPY main.go .

RUN CGO_ENABLED=0 GOOS=linux go build -o load-generator .

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/load-generator .

CMD ["./load-generator"]
