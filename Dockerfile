# Used by GoReleaser, which puts each platform's binary under $TARGETPLATFORM.
# The static base carries the CA certificates HTTPS needs.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/string /usr/bin/string
# A container's files go with it, so to string every run would be the first
# in a day and start an update check. The image is updated by pulling it.
ENV STRING_AUTO_UPDATE=0
ENTRYPOINT ["/usr/bin/string"]
