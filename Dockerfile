# syntax=docker/dockerfile:1
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S psoc && adduser -S -G psoc psoc
COPY bin/psoc /usr/local/bin/psoc
RUN chmod 0755 /usr/local/bin/psoc && mkdir -p /data && chown psoc:psoc /data
USER psoc
EXPOSE 8080
VOLUME ["/data"]
ENV PSOC_LISTEN=:8080 PSOC_DATA=/data/results.json
ENTRYPOINT ["/usr/local/bin/psoc"]
CMD ["serve"]
