FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN go build -o /out/authentik-rights-panel .

FROM alpine:3.20

WORKDIR /app
COPY --from=build /out/authentik-rights-panel /app/authentik-rights-panel
COPY config.example.json /app/config.example.json

ENV LISTEN_ADDR=0.0.0.0:8080
EXPOSE 8080

CMD ["/app/authentik-rights-panel"]
