package main

import (
	"net/http"
	"os"
	"strconv"

	"github.com/go-kit/kit/log/level"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/version"

	"github.com/hackbg/terra-chainlink-exporter/types"
)

const (
	ContractType = "flux_monitor"
)

var (
	ConstLabels    map[string]string
	RPC_ADDR       = os.Getenv("TERRA_RPC")
	TENDERMINT_URL = os.Getenv("TENDERMINT_URL")
	KAFKA_SERVER   = os.Getenv("KAFKA_SERVER")
	TOPIC          = os.Getenv("TOPIC")
	CONFIG_URL     = os.Getenv("CONFIG_URL")
	SERVICE_PORT   = os.Getenv("SERVICE_PORT")
)

func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

func init() {
	prometheus.MustRegister(version.NewCollector("terra_chainlink"))
	prometheus.MustRegister(fmAnswersTotal)
	prometheus.MustRegister(fmSubmissionsReceivedTotal)
	prometheus.MustRegister(fmSubmissionReceivedValuesGauge)
	prometheus.MustRegister(fmRoundsCounter)
	prometheus.MustRegister(nodeMetadataGauge)
	prometheus.MustRegister(fmLatestRoundResponsesGauge)
	prometheus.MustRegister(fmRoundAge)
}

func main() {
	promlogConfig := &promlog.Config{}
	logger := promlog.New(promlogConfig)

	feedManager, err := NewManager(TENDERMINT_URL, RPC_ADDR)
	if err != nil {
		level.Error(logger).Log("msg", "Could not initialize feed manager", "err", err)
		os.Exit(1)
	}
	defer feedManager.TendermintClient.Stop()

	msgs := make(chan types.Message)

	// Initialize the exporter
	kafkaWriter := newKafkaWriter(KAFKA_SERVER, TOPIC)
	exporter := NewExporter(*feedManager, logger, msgs, kafkaWriter)

	// Register the exporter
	prometheus.MustRegister(exporter)

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
