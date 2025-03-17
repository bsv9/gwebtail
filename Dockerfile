### BUILD IMAGE ###
FROM docker.io/golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod go.sum* ./

RUN if [ -f go.sum ]; then go mod download; else echo "skipping go mod download"; fi

COPY *.go ./
COPY index.html ./

RUN CGO_ENABLED=0 GOOS=linux go build -o gwebtail

### RELEASE IMAGE ###
FROM docker.io/alpine:3.21

RUN apk --no-cache add ca-certificates tzdata
RUN addgroup -S application --gid 10001 && adduser -S application -G application --uid 10001
RUN mkdir -p /logs && chown -R application:application /logs

WORKDIR /app

COPY --from=builder /app/gwebtail .
RUN chown -R application:application /app

USER application
ENV LOG_DIR=/logs
EXPOSE 8080
ENTRYPOINT ["/app/gwebtail"]

LABEL version="1.0.0"
LABEL description="gWebTail: Web-based log file viewer"
