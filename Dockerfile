# Built by GoReleaser, which drops the arch-appropriate static binary into the
# build context. Small image for the core TUI; mount a kubeconfig to use it, or
# run `--demo` with nothing mounted.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY tankertop /usr/local/bin/tankertop
ENTRYPOINT ["/usr/local/bin/tankertop"]
