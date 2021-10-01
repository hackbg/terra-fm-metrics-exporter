package exporter

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/hackbg/terra-chainlink-exporter/collector"
	"github.com/hackbg/terra-chainlink-exporter/manager"
	"github.com/hackbg/terra-chainlink-exporter/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	tmTypes "github.com/tendermint/tendermint/types"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

var (
	CONFIG_URL       = os.Getenv("CONFIG_URL")
	RPC_ADDR         = os.Getenv("TERRA_RPC")
	TENDERMINT_URL   = os.Getenv("TENDERMINT_URL")
	POLLING_INTERVAL = os.Getenv("POLLING_INTERVAL")
)

const (
	ContractType = "flux_monitor"
)

func StringToInt(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return i
}

var (
	// Counters
	FmAnswersTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_answers_total",
			Help: "Reports the number of on-chain answers for a feed",
		},
		[]string{"contract_address"},
	)
	FmSubmissionsReceivedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_submissions_received_total",
			Help: "Reports the current number of submissions",
		},
		[]string{"contract_address", "sender"},
	)
	FmRoundsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "flux_monitor_rounds",
			Help: "The number of rounds the monitor has observed on a feed",
		},
		[]string{"contract_address"},
	)
	// Gauges
	FmSubmissionReceivedValuesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_submission_received_values",
			Help: "Reports the current submission value for an oracle on a feed",
		},
		[]string{"contract_address", "sender"},
	)
	NodeMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "node_metadata",
			Help: "Exposes metdata for node",
		},
		[]string{"chain_id", "network_name", "oracle", "sender"},
	)
	FeedContractMetadataGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "feed_contract_metadata",
			Help: "Exposes metadata for individual feeds",
		},
		[]string{"chain_id", "contract_address", "contract_status", "contract_type", "feed_name", "feed_path", "network_id", "network_name", "symbol"},
	)
	FmAnswersGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_answers",
			Help: "Reports the current on chain price for a feed.",
		},
		[]string{"contract_address"},
	)
	FmCurrentHeadGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "head_tracker_current_head",
			Help: "Tracks the current block height that the monitoring instance has processed",
		},
		[]string{"network_name", "chain_id"},
	)
	// TODO:
	FmLatestRoundResponsesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_latest_round_responses",
			Help: "Reports the current number of submissions received for the latest round for a feed",
		},
		[]string{"contract_address"},
	)
	// TODO:
	FmRoundAge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_round_age",
			Help: "Indicates how many blocks have been mined after a round started where no answer has been produced",
		},
		[]string{"contract_address"},
	)
)

type Exporter struct {
	managers map[string]*manager.FeedManager

	WasmClient       wasmTypes.QueryClient
	TendermintClient *tmrpc.HTTP

	logger      log.Logger
	msgCh       chan types.Message
	mutex       sync.Mutex
	kafkaWriter *kafka.Writer

	pollingInterval time.Duration

	answersCounter          *prometheus.CounterVec
	answersGauge            *prometheus.GaugeVec
	submissionsCounter      *prometheus.CounterVec
	submissionsGauge        *prometheus.GaugeVec
	roundsCounter           *prometheus.CounterVec
	nodeMetadataGauge       *prometheus.GaugeVec
	contractMetadataGauge   *prometheus.GaugeVec
	currentHeadGauge        *prometheus.GaugeVec
	latetRoundResponseGauge *prometheus.GaugeVec
	roundAgeGauge           *prometheus.GaugeVec

	roundsEvents     []types.EventNewRound
	submissionEvents []types.EventSubmissionReceived
	answersEvents    []types.EventAnswerUpdated
}

func NewWasmClient() (wasmClient wasmTypes.QueryClient, err error) {
	grpcConn, err := grpc.Dial(
		RPC_ADDR,
		grpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	wasmClient = wasmTypes.NewQueryClient(grpcConn)
	return wasmClient, nil
}

func NewTendermintClient() (*tmrpc.HTTP, error) {
	client, err := tmrpc.New(TENDERMINT_URL, "/websocket")
	if err != nil {
		return nil, err
	}
	err = client.Start()
	if err != nil {
		return nil, err
	}

	return client, nil
}

func GetConfig(feeds map[string]types.FeedConfig) error {
	var config []types.FeedConfig
	response, err := http.Get(CONFIG_URL)
	if err != nil {
		return err
	}

	err = json.NewDecoder(response.Body).Decode(&config)
	defer response.Body.Close()

	if err != nil {
		return err
	}

	for _, feed := range config {
		feeds[feed.ContractAddress] = feed
	}

	return nil
}

func NewExporter(l log.Logger, ch chan types.Message, kafka *kafka.Writer, pollingInterval time.Duration) (*Exporter, error) {
	// create wasm client
	wasmClient, err := NewWasmClient()
	if err != nil {
		level.Error(l).Log("msg", "Could not create wasm client", "err", err)
		return nil, err
	}

	// create tendermint client
	// We use a client here to be able to fetch node data
	// Feed managers have their own tendermint client connections
	// to work around the max subs per client restriction
	tendermintClient, err := NewTendermintClient()
	if err != nil {
		level.Error(l).Log("msg", "Could not create tendermint client", "err", err)
		return nil, err
	}

	// fetch feed configurations
	feeds := make(map[string]types.FeedConfig)
	err = GetConfig(feeds)
	if err != nil {
		return nil, err
	}

	// create feed managers
	managers := make(map[string]*manager.FeedManager)
	for k, feed := range feeds {
		tendermintClient, err = NewTendermintClient()
		if err != nil {
			level.Error(l).Log("msg", "Could not create tendermint client for manager", "err", err)
			continue
		}
		feedManager := manager.NewManager(feed, tendermintClient, l)
		managers[k] = feedManager
	}

	e := Exporter{
		managers:         managers,
		WasmClient:       wasmClient,
		TendermintClient: tendermintClient,
		logger:           l,
		msgCh:            ch,
		mutex:            sync.Mutex{},
		kafkaWriter:      kafka,

		pollingInterval: pollingInterval,

		answersCounter: FmAnswersTotal,
		answersGauge:   FmAnswersGauge,

		submissionsCounter: FmSubmissionsReceivedTotal,
		submissionsGauge:   FmSubmissionReceivedValuesGauge,

		roundsCounter: FmRoundsCounter,

		nodeMetadataGauge:     NodeMetadataGauge,
		contractMetadataGauge: FeedContractMetadataGauge,

		currentHeadGauge:        FmCurrentHeadGauge,
		latetRoundResponseGauge: FmLatestRoundResponsesGauge,
		roundAgeGauge:           FmRoundAge,

		roundsEvents:     []types.EventNewRound{},
		submissionEvents: []types.EventSubmissionReceived{},
		answersEvents:    []types.EventAnswerUpdated{},
	}

	// subscribe to feeds events
	e.SubscribeFeeds(e.msgCh, e.logger)
	e.pollChanges()
	e.storeEvents(e.msgCh)

	return &e, nil
}

func (e *Exporter) subscribeFeed(ch chan types.Message, logger log.Logger, manager *manager.FeedManager) error {
	aggregator, err := manager.GetAggregator(e.WasmClient)
	if err != nil {
		level.Error(logger).Log("msg", "Could not get the aggregator address", "err", err)
		return err
	}
	// update the feed
	manager.Feed.Aggregator = *aggregator

	// sub proxy first
	err = manager.Subscribe(ch, logger, manager.Feed.ContractAddress)
	if err != nil {
		level.Error(logger).Log("msg", "Can't subscribe to proxy address", "err", err)
		return err
	}

	// sub aggregator
	err = manager.Subscribe(ch, logger, manager.Feed.Aggregator)
	if err != nil {
		level.Error(logger).Log("msg", "Can't subscribe to aggregator address", "err", err)
		return err
	}

	return nil
}

func (e *Exporter) SubscribeFeeds(ch chan types.Message, logger log.Logger) {
	for _, manager := range e.managers {
		err := e.subscribeFeed(ch, logger, manager)
		if err != nil {
			level.Error(logger).Log("msg", "Could not subscribe feed", "err", err)
			continue
		}
	}
}

// Describe describes all the metrics ever exporter (not including ones that should read from ws) by the Terra Chainlink exporter.
// It implements prometheus.Collector
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	e.contractMetadataGauge.Describe(ch)
	e.answersGauge.Describe(ch)
	e.currentHeadGauge.Describe(ch)
}

// Collect fetches the metrics
// It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mutex.Lock()

	e.collectLatestRoundData(ch)
	e.collectLatestBlockHeight(ch)
	e.collectAggregatorConfig(ch)
	e.collectRoundMetrics(ch)
	e.collectSubmissionMetrics(ch)
	e.collectAnswerMetrics(ch)

	e.mutex.Unlock()
}

func (e *Exporter) collectRoundMetrics(ch chan<- prometheus.Metric) bool {
	// aggregate the metrics
	for _, round := range e.roundsEvents {
		e.roundsCounter.With(prometheus.Labels{
			"contract_address": round.Feed,
		}).Inc()
	}
	// reset
	e.roundsEvents = nil

	return true
}

func (e *Exporter) collectSubmissionMetrics(ch chan<- prometheus.Metric) bool {
	for _, submission := range e.submissionEvents {
		e.submissionsCounter.With(prometheus.Labels{
			"contract_address": submission.Feed,
			"sender":           string(submission.Sender),
		}).Inc()

		e.submissionsGauge.With(prometheus.Labels{
			"contract_address": submission.Feed,
			"sender":           string(submission.Sender),
		}).Set(float64(submission.Submission.Key.Int64()))
	}
	e.submissionEvents = nil

	return true
}

func (e *Exporter) collectAnswerMetrics(ch chan<- prometheus.Metric) bool {
	for _, answer := range e.answersEvents {
		e.answersCounter.With(prometheus.Labels{
			"contract_address": answer.Feed,
		}).Inc()
	}
	e.answersEvents = nil

	return true
}

func (e *Exporter) collectLatestRoundData(ch chan<- prometheus.Metric) bool {
	for _, manager := range e.managers {
		response, err := e.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: manager.Feed.ContractAddress,
				QueryMsg:        []byte(`{"aggregator_query": {"get_latest_round_data": {}}}`),
			},
		)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't query the chain", "err", err)
			return false
		}

		var res types.LatestRoundResponse
		err = json.Unmarshal(response.QueryResult, &res)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't parse latest round response", "err", err)
			return false
		}
		e.answersGauge.WithLabelValues(manager.Feed.ContractAddress).Set(float64(StringToInt(res.Answer)))
		e.answersGauge.Collect(ch)
		e.answersGauge.Reset()
	}

	return true
}

func (e *Exporter) collectLatestBlockHeight(ch chan<- prometheus.Metric) bool {
	status, err := e.TendermintClient.Status(context.Background())
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't get the latest block height", "err", err)
		return false
	}

	e.currentHeadGauge.WithLabelValues(status.NodeInfo.Network, status.NodeInfo.Moniker).Set(float64(status.SyncInfo.LatestBlockHeight))
	e.currentHeadGauge.Collect(ch)
	e.currentHeadGauge.Reset()

	e.nodeMetadataGauge.WithLabelValues(status.NodeInfo.Network, status.NodeInfo.Moniker, "placeholder", "placeholder").Set(1)
	e.nodeMetadataGauge.Collect(ch)
	e.nodeMetadataGauge.Reset()

	return true
}

func (e *Exporter) collectAggregatorConfig(ch chan<- prometheus.Metric) bool {
	for _, manager := range e.managers {
		status, err := manager.TendermintClient.Status(context.Background())
		if err != nil {
			level.Error(e.logger).Log("msg", "Can't get the latest block height", "err", err)
			return false
		}
		config, err := e.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: manager.Feed.Aggregator,
				QueryMsg:        []byte(`{"get_aggregator_config": {}}`),
			},
		)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't query aggregator", "err", err)
			return false
		}

		var agggregatorConfig types.AggregatorConfigResponse
		err = json.Unmarshal(config.QueryResult, &agggregatorConfig)

		if err != nil {
			level.Error(e.logger).Log("msg", "Can't parse aggregator config", "err", err)
			return false
		}

		e.contractMetadataGauge.WithLabelValues(
			status.NodeInfo.Network,
			manager.Feed.Aggregator,
			manager.Feed.Status,
			ContractType,
			manager.Feed.Name,
			manager.Feed.Path,
			status.NodeInfo.Network,
			status.NodeInfo.Moniker,
			manager.Feed.Symbol,
		).Set(1)
		e.contractMetadataGauge.Collect(ch)
		e.contractMetadataGauge.Reset()

	}
	return true
}

func (e *Exporter) storeEvents(out chan types.Message) {
	handler := func(event types.EventRecords) {
		e.mutex.Lock()
		for _, round := range event.NewRound {
			res, err := json.Marshal(round)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not parse message", "err", err)
				continue
			}
			err = e.kafkaWriter.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("NewRound"),
					Value: res,
				},
			)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not write kafka message", "err", err)
				continue
			}
			level.Info(e.logger).Log("msg", "Got New Round event",
				"round", round.RoundId,
				"Feed", round.Feed)

			e.roundsEvents = append(e.roundsEvents, round)
			// fmLatestRoundResponsesGauge.With(prometheus.Labels{
			// 	"contract_address": m.Feed.ContractAddress,
			// }).Set(float64(m.latestRoundInfo.Submissions))
		}
		for _, round := range event.SubmissionReceived {
			res, err := json.Marshal(round)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not parse message", "err", err)
				continue
			}
			err = e.kafkaWriter.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("SubmissionReceived"),
					Value: res,
				},
			)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not write kafka message", "err", err)
				continue
			}
			level.Info(e.logger).Log("msg", "Got Submission Received event",
				"feed", round.Feed,
				"round id", round.RoundId,
				"submission", round.Submission.Key.Int64())

			e.submissionEvents = append(e.submissionEvents, round)
		}
		for _, update := range event.AnswerUpdated {
			res, err := json.Marshal(update)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not parse message", "err", err)
				continue
			}
			err = e.kafkaWriter.WriteMessages(context.Background(),
				kafka.Message{
					Key:   []byte("AnswerUpdated"),
					Value: res,
				},
			)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not write kafka message", "err", err)
				continue
			}
			level.Info(e.logger).Log("msg", "Got Answer Updated event",
				"feed", update.Feed,
				"round", update.RoundId,
				"Answer", update.Value.Key.Int64())

			e.answersEvents = append(e.answersEvents, update)
		}
		for _, confirm := range event.ConfirmAggregator {
			feed := e.managers[confirm.Feed].Feed
			// This event indicates that the aggregator address for a feed has changed
			// So we need to unsubscribe from old aggregator's events
			err := e.managers[confirm.Feed].Unsubscribe(e.logger, feed.Aggregator)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not unsubscribe old aggregator", "err", err)
				continue
			}

			// Update the feed's aggregator
			feed.Aggregator = confirm.NewAggregator
			e.managers[confirm.Feed].Feed = feed

			// And subscribe to new aggregator's events
			err = e.managers[confirm.Feed].Subscribe(e.msgCh, e.logger, confirm.NewAggregator)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not subscribe to new aggregator", "err", err)
				continue
			}
			level.Info(e.logger).Log("msg", "Got aggregator confirmed event",
				"feed", confirm.Feed,
				"new aggregator", confirm.NewAggregator)

		}
		e.mutex.Unlock()
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}
			info := collector.ExtractTxInfo(resp.Event.Data.(tmTypes.EventDataTx))
			eventRecords, err := collector.ParseEvents(
				resp.Event.Data.(tmTypes.EventDataTx).Result.Events,
				info,
				resp.Address,
			)
			if err != nil {
				continue
			}
			if eventRecords != nil {
				handler(*eventRecords)
			}
		}
	}()
}

func (e *Exporter) poll() {
	newFeeds := make(map[string]types.FeedConfig)
	ticker := time.NewTicker(e.pollingInterval)
	for range ticker.C {
		err := GetConfig(newFeeds)
		if err != nil {
			level.Error(e.logger).Log("msg", "Could not fetch new feed configurations", "err", err)
			continue
		}
		for _, feed := range newFeeds {
			e.mutex.Lock()
			_, present := e.managers[feed.ContractAddress]
			// if the feed is not present in the map of feeds, create new manager and subscribe to events
			if !present {
				level.Info(e.logger).Log("msg", "Found new feed", "name", feed.Name)
				tendermintClient, err := NewTendermintClient()
				if err != nil {
					level.Error(e.logger).Log("msg", "Could not create tendermint client for new Feed", "err", err)
					continue
				}
				manager := manager.NewManager(feed, tendermintClient, e.logger)
				e.managers[feed.ContractAddress] = manager

				err = e.subscribeFeed(e.msgCh, e.logger, e.managers[feed.ContractAddress])
				if err != nil {
					continue
				}
			}
			// if the feed exists, update the config
			e.managers[feed.ContractAddress].UpdateFeed(e.WasmClient, e.logger, feed)
			e.mutex.Unlock()
		}
	}
}

func (e *Exporter) pollChanges() {
	go e.poll()
}
