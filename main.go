package main

import (
	"net/http"
	"os"
	"time"

	"github.com/go-kit/kit/log/level"
	"github.com/segmentio/kafka-go"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/version"

	"github.com/hackbg/terra-chainlink-exporter/exporter"
	"github.com/hackbg/terra-chainlink-exporter/types"
)

func NewKafkaWriter(kafkaURL, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:     kafka.TCP(kafkaURL),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
}

var (
	ConstLabels      map[string]string
	RPC_ADDR         = os.Getenv("TERRA_RPC")
	TENDERMINT_URL   = os.Getenv("TENDERMINT_URL")
	KAFKA_SERVER     = os.Getenv("KAFKA_SERVER")
	TOPIC            = os.Getenv("TOPIC")
	SERVICE_PORT     = os.Getenv("SERVICE_PORT")
	POLLING_INTERVAL = os.Getenv("POLLING_INTERVAL")
)

// register metrics
func init() {
	prometheus.MustRegister(version.NewCollector("terra_chainlink"))
	prometheus.MustRegister(exporter.FmAnswersTotal)
	prometheus.MustRegister(exporter.FmSubmissionsReceivedTotal)
	prometheus.MustRegister(exporter.FmSubmissionReceivedValuesGauge)
	prometheus.MustRegister(exporter.FmRoundsCounter)
	prometheus.MustRegister(exporter.FmLatestRoundResponsesGauge)
	prometheus.MustRegister(exporter.FmRoundAge)
	prometheus.MustRegister(exporter.FmAnswersGauge)
	prometheus.MustRegister(exporter.FmCurrentHeadGauge)
	prometheus.MustRegister(exporter.NodeMetadataGauge)
	prometheus.MustRegister(exporter.FeedContractMetadataGauge)
}

func main() {
	promlogConfig := &promlog.Config{}
	logger := promlog.New(promlogConfig)

	msgs := make(chan types.Message)

	if POLLING_INTERVAL == "" {
		POLLING_INTERVAL = "5s"
	}
	pollingInterval, err := time.ParseDuration(POLLING_INTERVAL)

	if err != nil {
		level.Error(logger).Log("msg", "Could not set the polling interval", "err", err)
		os.Exit(1)
	}

	// Initialize the exporter
	kafkaWriter := NewKafkaWriter(KAFKA_SERVER, TOPIC)
	_, err = exporter.NewExporter(logger, msgs, kafkaWriter, pollingInterval)
	if err != nil {
		level.Error(logger).Log("msg", "Could not create the exporter", "err", err)
		return
	}

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// need more specific check
		w.Write([]byte("ok"))
	})

	if SERVICE_PORT == "" {
		level.Info(logger).Log("msg", "Defaulting to 8089")
		SERVICE_PORT = "8089"
	}

	level.Info(logger).Log("msg", "Listening on port 8089")
	err = http.ListenAndServe(":"+SERVICE_PORT, nil)

	if err != nil {
		level.Error(logger).Log("msg", "Error starting HTTP server", "err", err)
		os.Exit(1)
	}
}
