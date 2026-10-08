# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/firekeeper .

FROM alpine:3.22
# sqlite3 is for owner-run backups (see docs/self-hosting.md); the server does not use it.
RUN apk add --no-cache ca-certificates sqlite \
    && adduser -D -u 10001 -h /home/firekeeper firekeeper \
    && mkdir /data && chown firekeeper:firekeeper /data
COPY --from=build /out/firekeeper /usr/local/bin/firekeeper
USER firekeeper
ENV HOME=/home/firekeeper
VOLUME /data
EXPOSE 7777
ENTRYPOINT ["firekeeper"]
CMD ["serve", "--listen", "0.0.0.0:7777", "--db", "/data/firekeeper.db"]
