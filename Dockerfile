FROM node:22-alpine AS webui

WORKDIR /webui
COPY webui/package*.json ./
RUN npm ci
COPY webui/ .
RUN npm run build

FROM golang:1.26-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=webui /webui/dist ./webui/dist
RUN CGO_ENABLED=0 go build -tags "with_utls prod" -ldflags "-s -w" -o http-proxy .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /build/http-proxy .

EXPOSE 9090

ENTRYPOINT ["/app/http-proxy"]
