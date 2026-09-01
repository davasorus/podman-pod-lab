# Build a static binary, ship it on scratch.
FROM docker.io/library/golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /shortener .

FROM scratch
COPY --from=build /shortener /shortener
# Default listen port (override with PORT env). Keep EXPOSE in sync with it.
EXPOSE 8080
# Run as a non-root uid (scratch has no users; numeric id works).
USER 65534:65534
ENTRYPOINT ["/shortener"]
