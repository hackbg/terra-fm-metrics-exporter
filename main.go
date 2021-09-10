package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/google/uuid"
	"github.com/hackbg/terra-chainlink-exporter/collector"
	"github.com/hackbg/terra-chainlink-exporter/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/segmentio/kafka-go"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	tmTypes "github.com/tendermint/tendermint/types"
)

const (
	ContractType = "flux_monitor"
)

var (
	ChainId        string
	Moniker        string
	ConstLabels    map[string]string
	RPC_ADDR       = os.Getenv("TERRA_RPC")
	WS_URL         = os.Getenv("WS_URL")
	TENDERMINT_RPC = os.Getenv("TENDERMINT_RPC")
	KAFKA_SERVER   = os.Getenv("KAFKA_SERVER")
	TOPIC          = os.Getenv("TOPIC")
	CONFIG_URL     = os.Getenv("CONFIG_URL")
)

var (
	// Counters
	fmAnswersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_answers_total",
			Help: "Reports the number of on-chain answers for a feed",
		},
		[]string{"contract_address"},
	)
	fmSubmissionsReceivedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_submissions_received_total",
			Help: "Reports the current number of submissions",
		},
		[]string{"contract_address", "sender"},
	)
	fmRoundsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_rounds",
			Help: "The number of rounds the monitor has observed on a feed",
		},
		[]string{"contract_address"},
	)
	// Gauges
	fmSubmissionReceivedValuesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_submission_received_values",
			Help: "Reports the current submission value for an oracle on a feed",
		},
		[]string{"contract_address", "sender"},
	)
	nodeMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "node_metadata",
			Help:        "Exposes metdata for node",
			ConstLabels: ConstLabels,
		},
		[]string{"chain_id", "network_name"},
	)
	feedContractMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feed_contract_metadata",
			Help: "Exposes metadata for individual feeds",
		},
		[]string{"chain_id", "contract_address", "contract_status", "contract_type", "feed_name", "feed_path", "network_id", "network_name", "symbol"},
	)

	fmAnswersGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_answers",
			Help: "Reports the current on chain price for a feed.",
		},
		[]string{"contract_address"},
	)

	fmCurrentHeadGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "head_tracker_current_head",
			Help: "Tracks the current block height that the monitoring instance has processed",
		},
		[]string{"network_name", "chain_id"},
	)
	fmLatestRoundResponsesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_latest_round_responses",
			Help: "Reports the current number of submissions received for the latest round for a feed",
		},
		[]string{"contract_address"},
	)
)

var log = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()

func newKafkaWriter(kafkaUrl, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:     kafka.TCP(kafkaUrl),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
}

type manager struct {
	Feed            types.Feed
	latestRoundInfo types.LatestRoundInfo
	collector       collector.Collector
	writer          *kafka.Writer
}

func (m *manager) Stop() {
	// TODO:
}

func createManager(feed types.Feed, client tmrpc.HTTP) (*manager, error) {
	fmt.Printf("Creating a feed with address: %s and name: %s\n", feed.ContractAddress, feed.Name)
	collector, err := collector.NewCollector(client)
	writer := newKafkaWriter(KAFKA_SERVER, TOPIC)
	latestRoundInfo := types.LatestRoundInfo{RoundId: 0, Submissions: 0}

	if err != nil {
		return nil, err
	}

	return &manager{
		Feed:            feed,
		collector:       *collector,
		writer:          writer,
		latestRoundInfo: latestRoundInfo,
	}, nil
}

type message struct {
	manager *manager
	event   ctypes.ResultEvent
}

func (m *manager) subscribe(messages chan<- message) {
	subQuery := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", m.Feed.ContractAddress)

	out, _ := m.collector.Subscribe(context.Background(), "subscribe", subQuery)

	for {
		resp, ok := <-out
		if !ok {
			return
		}
		msg := message{manager: m, event: resp}
		messages <- msg
	}
}

func (m *manager) unsubscribe() {
	subQuery := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", m.Feed.ContractAddress)

	err := m.collector.Unsubscribe(context.Background(), "unsubscribe", subQuery)
	if err != nil {
		fmt.Println(err)
	}
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

func setChainId(client tmrpc.HTTP) {
	status, err := client.Status(context.Background())
	if err != nil {
		log.Fatal().Err(err).Msg("Could not query Tendermint status")
	}
	log.Info().Str("network", status.NodeInfo.Network).Msg("Got network status from Tendermint")
	ChainId = status.NodeInfo.Network
	Moniker = status.NodeInfo.Moniker
	fmt.Printf("chain: %s, moniker: %s\n", ChainId, Moniker)

	ConstLabels = map[string]string{
		"chain_id":      ChainId,
		"contract_type": ContractType,
		"network_name":  Moniker,
	}

	nodeMetadataGauge.With(prometheus.Labels{
		"chain_id":     ChainId,
		"network_name": Moniker,
	}).Set(1)
}

func TerraChainlinkHandler(w http.ResponseWriter, r *http.Request, managers *Managers) {
	sublogger := log.With().
		Str("request-id", uuid.New().String()).
		Logger()

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
	managers.mu.Lock()
	defer managers.mu.Unlock()

	// TODO:
	// go func() {
	// 	defer wg.Done()
	// 	sublogger.Debug().Msg("Querying the node information")
	// 	height := collector.GetLatestBlockHeight(managers[0].collector)
	// 	fmCurrentHeadGauge.With(prometheus.Labels{
	// 		"chain_id":     ChainId,
	// 		"network_name": Moniker,
	// 	}).Set(float64(height))
	// }()
	// wg.Add(1)

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Started querying aggregator answers")

		for _, manager := range managers.m {
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

		}
	}()
	wg.Add(1)

	go func() {
		defer wg.Done()
		sublogger.Debug().Msg("Querying aggregators metadata")

		for _, manager := range managers.m {
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
				"network_id":       ChainId,
				"network_name":     Moniker,
				"symbol":           manager.Feed.Symbol,
			}).Set(1)
		}
	}()
	wg.Add(1)
	wg.Wait()

	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	h.ServeHTTP(w, r)
}

func consume(out <-chan message) {
	handler := func(event types.EventRecords, m *manager) {
		for _, round := range event.NewRound {
			res, err := json.Marshal(round)
			if err != nil {
				continue
			}
			err = m.writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("NewRound"),
					Value: res,
				},
			)
			if err != nil {
				fmt.Println(err)
			}

			m.latestRoundInfo.RoundId = round.RoundId
			m.latestRoundInfo.Submissions = 0

			fmRoundsCounter.With(prometheus.Labels{
				"contract_address": m.Feed.ContractAddress,
			}).Inc()
			fmLatestRoundResponsesGauge.With(prometheus.Labels{
				"contract_address": m.Feed.ContractAddress,
			}).Set(float64(m.latestRoundInfo.Submissions))
		}
		for _, round := range event.SubmissionReceived {
			res, err := json.Marshal(round)
			if err != nil {
				continue
			}
			// write kafka message
			err = m.writer.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("Submission Received"),
					Value: res,
				},
			)
			if err != nil {
				log.Debug().Err(err).Msg("Could not send message to kafka")
			}

			if round.RoundId == m.latestRoundInfo.RoundId {
				m.latestRoundInfo.Submissions += 1
			}

			// set counters and gauges
			fmLatestRoundResponsesGauge.With(prometheus.Labels{
				"contract_address": m.Feed.ContractAddress,
			}).Set(float64(m.latestRoundInfo.Submissions))
			fmSubmissionsReceivedTotal.With(prometheus.Labels{
				"contract_address": m.Feed.ContractAddress,
				"sender":           string(round.Sender),
			}).Inc()
			fmSubmissionReceivedValuesGauge.With(prometheus.Labels{
				"contract_address": m.Feed.ContractAddress,
				"sender":           string(round.Sender),
			}).Set(float64(round.Submission.Key.Int64()))

		}
		for _, update := range event.AnswerUpdated {
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
			fmAnswersTotal.With(prometheus.Labels{
				"contract_address": m.Feed.ContractAddress,
			}).Inc()
		}
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}
			info := collector.ExtractTxInfo(resp.event.Data.(tmTypes.EventDataTx))
			eventRecords, err := collector.ParseEvents(resp.event.Data.(tmTypes.EventDataTx).Result.Events, info)
			if err != nil {
				continue
			}
			if eventRecords != nil {
				handler(*eventRecords, resp.manager)
			}
		}
	}()
}

func getConfig() ([]types.Feed, error) {
	var config []types.Feed
	response, err := http.Get(CONFIG_URL)
	if err != nil {
		return nil, err
	}

	json.NewDecoder(response.Body).Decode(&config)
	defer response.Body.Close()

	if err != nil {
		return nil, err
	}

	return config, nil
}

func poll(managers *Managers) {
	ticker := time.NewTicker(3 * time.Second)
	for range ticker.C {
		data, err := getConfig()
		if err != nil {
			fmt.Println(err)
		}

		for _, feed := range data {
			// TODO: unsubscribe somewhere here if the feed is not live?
			managers.mu.Lock()
			res := cmp.Equal(managers.m[feed.ContractAddress].Feed, feed)
			if !res {
				managers.m[feed.ContractAddress].Feed = feed
			}
			managers.mu.Unlock()
		}
	}
}

type Managers struct {
	mu sync.RWMutex
	m  map[string]*manager
}

func main() {
	client, err := tmrpc.New(TENDERMINT_RPC, "/websocket")

	if err != nil {
		log.Fatal().Err(err).Msg("Could not create Tendermint Client")
	}

	setChainId(*client)

	err = client.Start()
	if err != nil {
		panic("Could not start the client")
	}
	defer client.Stop()

	feeds, err := getConfig()

	if err != nil {
		log.Fatal().Err(err).Msg("Could not parse")
	}

	msgs := make(chan message)

	managers := make(map[string]*manager)
	sharedManagers := Managers{mu: sync.RWMutex{}, m: managers}

	for _, feed := range feeds {
		feedManager, err := createManager(feed, *client)
		sharedManagers.m[feed.ContractAddress] = feedManager

		if err != nil {
			log.Fatal().Err(err).Msg("Could not create feed manager")
		}
		go sharedManagers.m[feed.ContractAddress].subscribe(msgs)
	}

	go consume(msgs)
	go poll(&sharedManagers)

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		TerraChainlinkHandler(w, r, &sharedManagers)
	})

	err = http.ListenAndServe(":8089", nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not start application")
	}
}
