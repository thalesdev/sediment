# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.4

FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY compat/go.mod compat/go.sum ./compat/
COPY cmd/sedimentd/go.mod cmd/sedimentd/go.sum ./cmd/sedimentd/
RUN cd cmd/sedimentd && go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN cd cmd/sedimentd && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/sedimentd .
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sedimentd /usr/local/bin/sedimentd
COPY --from=build --chown=nonroot:nonroot /out/data /data
COPY packaging/sediment.conf /etc/sediment/sediment.conf
VOLUME /data
EXPOSE 6379
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=4s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/sedimentd", "healthcheck", "-config", "/etc/sediment/sediment.conf"]
ENTRYPOINT ["/usr/local/bin/sedimentd"]
CMD ["-config", "/etc/sediment/sediment.conf"]
