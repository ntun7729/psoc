# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go dashboard.html ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/psoc .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S psoc && adduser -S -G psoc psoc
COPY --from=build /out/psoc /usr/local/bin/psoc
RUN mkdir -p /data && chown psoc:psoc /data
USER psoc
EXPOSE 8080
VOLUME ["/data"]
ENV PSOC_LISTEN=:8080 PSOC_DATA=/data/results.json
ENTRYPOINT ["/usr/local/bin/psoc"]
CMD ["serve"]
