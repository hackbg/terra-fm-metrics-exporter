FROM golang:1.16-alpine3.12 AS go-builder
RUN apk add build-base
WORKDIR /
COPY . .


ADD https://github.com/CosmWasm/wasmvm/releases/download/v0.15.1/libwasmvm_muslc.a /lib/libwasmvm_muslc.a
RUN sha256sum /lib/libwasmvm_muslc.a | grep 379c61d2e53f87f63639eaa8ba8bbe687e5833158bb10e0dc0523c3377248d01

RUN go build -mod=readonly -tags "muslc make build" -o bin/terra-chainlink-exporter

FROM scratch
COPY --from=go-builder /bin/terra-chainlink-exporter /bin/terra-chainlink-exporter

EXPOSE 8080

ENTRYPOINT ["/bin/terra-chainlink-exporter"]