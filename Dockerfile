# Built by GoReleaser, which drops the arch-appropriate static binary into the
# build context. Small image for the core TUI; mount a kubeconfig to use it, or
# run `--demo` with nothing mounted.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 tankertop
COPY tankertop /usr/local/bin/tankertop
# Run as a non-root user by default; the TUI needs no privileges of its own and
# uses whatever kubeconfig/engine socket you mount in.
USER tankertop
ENTRYPOINT ["/usr/local/bin/tankertop"]
