# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api

# ---- run stage ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
# Migrations are embedded in the binary (see db/embed.go), so nothing else
# needs to be copied.
COPY --from=build /out/api /app/api
EXPOSE 8080
ENTRYPOINT ["/app/api"]
