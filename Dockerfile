# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api
# The ops bot ships in the same image but runs as its own container, so a
# crashed API cannot take the bot down with it.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/bot ./cmd/bot
# The finance bot: a separate product with its own database, same image.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/finbot ./cmd/finbot
# The parser tutor: run by hand (docs/parsertutor.md), never as a service.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/parsertutor ./cmd/parsertutor
# The MCP server: started per session by an MCP client over SSH, inside the
# running api container (docs/portfolio-mcp.md). Not a service.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/portfolio-mcp ./cmd/portfolio-mcp

# ---- run stage ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
# Migrations are embedded in the binary (see db/embed.go), so nothing else
# needs to be copied.
COPY --from=build /out/api /app/api
COPY --from=build /out/bot /app/bot
COPY --from=build /out/finbot /app/finbot
COPY --from=build /out/parsertutor /app/parsertutor
COPY --from=build /out/portfolio-mcp /app/portfolio-mcp
EXPOSE 8080
ENTRYPOINT ["/app/api"]
