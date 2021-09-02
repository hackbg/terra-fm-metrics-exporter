package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/hackbg/terra-chainlink-exporter/collector"
	"github.com/hackbg/terra-chainlink-exporter/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/segmentio/kafka-go"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	tmTypes "github.com/tendermint/tendermint/types"
)

const (
	ContractType = "flux_monitor"
)

var (
	ChainId        string
	ConstLabels    map[string]string
	RPC_ADDR       = os.Getenv("TERRA_RPC")
	WS_URL         = os.Getenv("WS_URL")
	TENDERMINT_RPC = os.Getenv("TENDERMINT_RPC")
	KAFKA_SERVER   = os.Getenv("KAFKA_SERVER")
	TOPIC          = os.Getenv("TOPIC")
)

var log = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

func newKafkaWriter(kafkaUrl, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:     kafka.TCP(kafkaUrl),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
}

type Feed struct {
	ContractAddress string
	ContractVersion int
	DecimalPlaces   int
	Heartbeat       int64
	History         bool
	Multiply        string
	Name            string
	Symbol          string
	Pair            []string
	Path            string
	NodeCount       int
	Status          string
}

type manager struct {
	Feed
	collector collector.Collector
	writer    *kafka.Writer
}

func (m *manager) Stop() {
	// TODO
}

func createManager(feed Feed) (*manager, error) {
	fmt.Printf("Creating a feed with address: %s and name: %s", feed.ContractAddress, feed.Name)
	collector, err := collector.NewCollector()
	writer := newKafkaWriter(KAFKA_SERVER, TOPIC)

	if err != nil {
		return nil, err
	}

	return &manager{
		Feed:      feed,
		collector: *collector,
		writer:    writer,
	}, nil
}

func (m *manager) subscribe() error {
	subQuery := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", m.Feed.ContractAddress)
	out, err := m.collector.Subscribe(context.Background(), "subscribe", subQuery)

	handler := func(event types.EventRecords) {
		for _, round := range event.NewRound {
			res, err := json.Marshal(round)
			if err != nil {
				continue
			}
			fmt.Println(round)
			err = m.writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("NewRound"),
					Value: res,
				},
			)
			if err != nil {
				fmt.Println(err)
			}
			fmt.Println("WRITTEN TO NEW ROUND")
		}
		for _, round := range event.SubmissionReceived {
			fmt.Println(round)
			res, err := json.Marshal(round)
			if err != nil {
				continue
			}
			err = m.writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("Submission Received"),
					Value: res,
				},
			)
			if err != nil {
				fmt.Println(err)
			}
			fmt.Println("WRITTEN SUBMISSION")
		}
		for _, update := range event.AnswerUpdated {
			fmt.Println(update)
			res, err := json.Marshal(update)
			if err != nil {
				continue
			}
			err = m.writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("Answer Updated"),
					Value: res,
				},
			)
			if err != nil {
				fmt.Println(err)
			}
			fmt.Println("WRITTEN SUBMISSION")
		}
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}
			info := collector.ExtractTxInfo(resp.Data.(tmTypes.EventDataTx))
			if err != nil {
				continue
			}
			eventRecords, err := collector.ParseEvents(resp.Data.(tmTypes.EventDataTx).Result.Events, info)
			if err != nil {
				continue
			}
			if eventRecords != nil {
				handler(*eventRecords)
			}
		}
	}()

	return nil
}

// Terra responses
type (
	AggregatorConfigResponse struct {
		Link               string `json:"link,omitempty"`
		Validator          string `json:"validator,omitempty"`
		PaymentAmount      string `json:"payment_amount,omitempty"`
		MaxSubmissionCount int    `json:"max_submission_count,omitempty"`
		MinSubmissionCount int    `json:"min_submission_count,omitempty"`
		RestartDelay       int    `json:"restart_delay,omitempty"`
		Timeout            int    `json:"timeout,omitempty"`
		Decimals           int    `json:"decimal,omitempty"`
		Description        string `json:"description,omitempty"`
		MinSubmissionValue string `json:"min_submission_value,omitempty"`
		MaxSubmissionValue string `json:"max_submission_value,omitempty"`
	}
	LatestRoundResponse struct {
		RoundId         int    `json:"round_id"`
		Answer          string `json:"answer"`
		StartedAt       int    `json:"started_at"`
		UpdatedAt       int    `json:"updated_at"`
		AnsweredInRound int    `json:"answered_in_round"`
	}
)

func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		fmt.Println(err)
	}
	return i
}

func setChainId() {
	client, err := tmrpc.New(TENDERMINT_RPC, "/websocket")
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create Tendermint client")
	}

	status, err := client.Status(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Could not query Tendermint status")
	}

	log.Info().Str("network", status.NodeInfo.Network).Msg("Got network status from Tendermint")
	ChainId = status.NodeInfo.Network
	ConstLabels = map[string]string{
		"chain_id":      ChainId,
		"contract_type": ContractType,
	}
}

func TerraChainlinkHandler(w http.ResponseWriter, r *http.Request, managers []manager) {
	sublogger := log.With().
		Str("request-id", uuid.New().String()).
		Logger()

	// Gauges
	nodeMetadataGauge := prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name:        "node_metadata",
			Help:        "Exposes metdata for node",
			ConstLabels: ConstLabels,
		},
	)

	// TODO
	feedContractMetadataGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feed_contract_metadata",
			Help: "Exposes metadata for individual feeds",
		},
		[]string{"chain_id", "contract_address", "contract_status", "contract_type", "feed_name", "feed_path", "network_id", "network_name", "symbol"},
	)

	fmAnswersGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_answers",
			Help: "Reports the current on chain price for a feed.",
		},
		[]string{"contract_address"},
	)

	fmCurrentHeadGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "head_tracker_current_head",
			Help: "Tracks the current block height that the monitoring instance has processed",
		},
		[]string{"network_name", "chain_id", "network_id"},
	)

	// TODO
	fmSubmissionReceivedValuesGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_submission_received_values",
			Help: "Reports the current submission value for an oracle on a feed",
		},
		[]string{"contract_address", "sender"},
	)

	// TODO: should be doable?
	fmLatestRoundResponsesGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_latest_round_responses",
			Help: "Reports the current number of submissions received for the latest round for a feed",
		},
		[]string{"contract_address"},
	)

	fmAnswersTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_answers_total",
			Help: "Reports the number of on-chain answers for a feed",
		},
		[]string{"contract_address"},
	)

	// TODO, maybe not possible
	fmSubmissionsReceivedTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_submissions_received_total",
			Help: "Reports the current number of submissions",
		},
		[]string{"contract_address", "sender"},
	)

	// Should be okay
	fmRoundsCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_rounds",
			Help: "The number of rounds the monitor has observed on a feed",
		},
		[]string{},
	)

	// Register metrics
	registry := prometheus.NewRegistry()
	registry.MustRegister(fmAnswersGauge)
	registry.MustRegister(fmLatestRoundResponsesGauge)
	registry.MustRegister(nodeMetadataGauge)
	registry.MustRegister(fmAnswersTotal)
	registry.MustRegister(fmCurrentHeadGauge)
	registry.MustRegister(feedContractMetadataGauge)
	registry.MustRegister(fmSubmissionReceivedValuesGauge)
	registry.MustRegister(fmSubmissionsReceivedTotal)
	registry.MustRegister(fmRoundsCounter)

	var wg sync.WaitGroup

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Querying the node information")
		// TODO
		height := collector.GetLatestBlockHeight(managers[0].collector)
		fmCurrentHeadGauge.With(prometheus.Labels{
			"chain_id": ChainId,
			// TODO
			"network_name": "",
			"network_id":   "",
		}).Set(float64(height))
	}()
	wg.Add(1)

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Started querying aggregator answers")

		for _, manager := range managers {
			response, err := manager.collector.GetLatestRoundData(manager.Feed.ContractAddress)

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not query the contract")
				return
			}

			var res LatestRoundResponse
			err = json.Unmarshal(response.QueryResult, &res)

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not parse the response")
				return
			}

			fmAnswersGauge.With(prometheus.Labels{
				"contract_address": manager.Feed.ContractAddress,
			}).Set(float64(StringToInt(res.Answer)))

			fmAnswersTotal.With(prometheus.Labels{
				"contract_address": manager.Feed.ContractAddress,
			}).Add(float64(res.RoundId))
		}
	}()
	wg.Add(1)

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Querying aggregators metadata")

		for _, manager := range managers {
			response, err := manager.collector.GetAggregatorConfig(manager.Feed.ContractAddress)

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not query the contract")
				return
			}

			var res AggregatorConfigResponse
			err = json.Unmarshal(response.QueryResult, &res)

			if err != nil {
				sublogger.Error().Err(err).Msg("Could not parse the response")
				return
			}

			feedContractMetadataGauge.With(prometheus.Labels{
				"chain_id":         ChainId,
				"contract_address": manager.Feed.ContractAddress,
				"contract_status":  manager.Feed.Status,
				"contract_type":    ContractType,
				"feed_name":        manager.Feed.Name,
				"feed_path":        manager.Feed.Path,
				"network_id":       "",
				"network_name":     "",
				"symbol":           manager.Feed.Symbol,
			}).Set(1)
		}
	}()
	wg.Add(1)
	wg.Wait()

	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	h.ServeHTTP(w, r)
}

func main() {
	feed := Feed{
		ContractAddress: "terra1tndcaqxkpc5ce9qee5ggqf430mr2z3pefe5wj6",
		ContractVersion: 1,
		DecimalPlaces:   8,
		Heartbeat:       100,
		History:         false,
		Multiply:        "1000000",
		Name:            "LINK / USD",
		Symbol:          "$",
		Pair:            []string{"LINK", "USD"},
		Path:            "link_usd",
		NodeCount:       1,
		Status:          "live",
	}
	feed1 := Feed{
		ContractAddress: "terra1z449mpul3pwkdd3892gv28ewv5l06w7895wewm",
		ContractVersion: 1,
		DecimalPlaces:   8,
		Heartbeat:       100,
		History:         false,
		Multiply:        "1000000",
		Name:            "LUNA / USD",
		Symbol:          "$",
		Pair:            []string{"LUNA", "USD"},
		Path:            "luna_usd",
		NodeCount:       1,
		Status:          "live",
	}

	feedManager, err := createManager(feed)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create feed manager")
	}
	defer feedManager.collector.TendermintClient.Stop()

	err = feedManager.subscribe()

	if err != nil {
		log.Fatal().Err(err).Msg("Could not create feed manager")
	}

	feedManager1, err := createManager(feed1)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create feed manager")
	}
	defer feedManager1.collector.TendermintClient.Stop()

	err = feedManager1.subscribe()

	if err != nil {
		log.Fatal().Err(err).Msg("Could not create feed manager")
	}

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		TerraChainlinkHandler(w, r, []manager{*feedManager, *feedManager1})
	})

	err = http.ListenAndServe(":8089", nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not start application")
	}
}
