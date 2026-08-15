FROM docker.io/library/golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o /shortener .

FROM scratch
COPY --from=build /shortener /shortener
EXPOSE 8080
ENTRYPOINT ["/shortener"]