module github.com/hackbg/terra-chainlink-exporter

go 1.16

require (
	github.com/go-kit/kit v0.10.0 // indirect
	github.com/google/go-cmp v0.5.6 // indirect
	github.com/google/uuid v1.3.0
	github.com/prometheus/client_golang v1.11.0
	github.com/prometheus/common v0.30.0
	github.com/rs/zerolog v1.23.0
	github.com/segmentio/kafka-go v0.4.17
	github.com/tendermint/tendermint v0.34.11
	github.com/terra-money/core v0.5.0-rc0
	google.golang.org/grpc v1.39.0
)

replace google.golang.org/grpc => google.golang.org/grpc v1.33.2

replace github.com/gogo/protobuf => github.com/regen-network/protobuf v1.3.3-alpha.regen.1
