module github.com/hackbg/terra-chainlink-exporter

go 1.16

require (
	github.com/google/uuid v1.3.0
	github.com/gorilla/websocket v1.4.2
	github.com/prometheus/client_golang v1.11.0
	github.com/rs/zerolog v1.23.0
	github.com/segmentio/kafka-go v0.4.17
	github.com/tendermint/tendermint v0.34.11
	github.com/terra-money/core v0.5.0-rc0
	github.com/tidwall/gjson v1.6.7
	go.uber.org/atomic v1.9.0
	google.golang.org/grpc v1.39.0
)

replace google.golang.org/grpc => google.golang.org/grpc v1.33.2

replace github.com/gogo/protobuf => github.com/regen-network/protobuf v1.3.3-alpha.regen.1
