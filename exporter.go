package main

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/hackbg/terra-chainlink-exporter/collector"
	"github.com/hackbg/terra-chainlink-exporter/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	tmTypes "github.com/tendermint/tendermint/types"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
)

func newKafkaWriter(kafkaURL, topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:     kafka.TCP(kafkaURL),
		Topic:    topic,
		Balancer: &kafka.LeastBytes{},
	}
}

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
			Name: "node_metadata",
			Help: "Exposes metdata for node",
		},
		[]string{"chain_id", "network_name", "oracle", "sender"},
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
	// TODO:
	fmLatestRoundResponsesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_latest_round_responses",
			Help: "Reports the current number of submissions received for the latest round for a feed",
		},
		[]string{"contract_address"},
	)
	// TODO:
	fmRoundAge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "flux_monitor_round_age",
			Help: "Indicates how many blocks have been mined after a round started where no answer has been produced",
		},
		[]string{"contract_address"},
	)
)

type Exporter struct {
	feedManager FeedManager
	logger      log.Logger
	msgCh       chan types.Message
	mutex       sync.Mutex
	kafkaWriter *kafka.Writer

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

func NewExporter(fm FeedManager, l log.Logger, ch chan types.Message, kafka *kafka.Writer) (*Exporter, error) {
	e := Exporter{
		feedManager: fm,
		logger:      l,
		msgCh:       ch,
		mutex:       sync.Mutex{},
		kafkaWriter: kafka,

		answersCounter: fmAnswersTotal,
		answersGauge:   fmAnswersGauge,

		submissionsCounter: fmSubmissionsReceivedTotal,
		submissionsGauge:   fmSubmissionReceivedValuesGauge,

		roundsCounter: fmRoundsCounter,

		nodeMetadataGauge:     nodeMetadataGauge,
		contractMetadataGauge: feedContractMetadataGauge,

		currentHeadGauge:        fmCurrentHeadGauge,
		latetRoundResponseGauge: fmLatestRoundResponsesGauge,
		roundAgeGauge:           fmRoundAge,

		roundsEvents:     []types.EventNewRound{},
		submissionEvents: []types.EventSubmissionReceived{},
		answersEvents:    []types.EventAnswerUpdated{},
	}

	err := e.feedManager.initializeFeeds(e.msgCh, e.logger)
	if err != nil {
		level.Error(l).Log("msg", "Could not initialize feeds", "err", err)
		return nil, err
	}
	e.pollChanges()
	e.storeEvents(e.msgCh)

	return &e, nil
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
		e.answersCounter.Collect(ch)
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
		e.submissionsCounter.Collect(ch)

		e.submissionsGauge.With(prometheus.Labels{
			"contract_address": submission.Feed,
			"sender":           string(submission.Sender),
		}).Set(float64(submission.Submission.Key.Int64()))
		e.submissionsGauge.Collect(ch)
	}
	e.submissionEvents = nil

	return true
}

func (e *Exporter) collectAnswerMetrics(ch chan<- prometheus.Metric) bool {
	for _, answer := range e.answersEvents {
		e.answersCounter.With(prometheus.Labels{
			"contract_address": answer.Feed,
		}).Inc()
		e.answersCounter.Collect(ch)
	}
	e.answersEvents = nil

	return true
}

func (e *Exporter) collectLatestRoundData(ch chan<- prometheus.Metric) bool {
	for _, feed := range e.feedManager.Feeds {
		response, err := e.feedManager.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: feed.ContractAddress,
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
		e.answersGauge.WithLabelValues(feed.ContractAddress).Set(float64(StringToInt(res.Answer)))
		e.answersGauge.Collect(ch)
		e.answersGauge.Reset()
	}

	return true
}

func (e *Exporter) collectLatestBlockHeight(ch chan<- prometheus.Metric) bool {
	status, err := e.feedManager.TendermintClient.Status(context.Background())
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't get the latest block height", "err", err)
		return false
	}

	level.Info(e.logger).Log("network", status.NodeInfo.Network)

	e.currentHeadGauge.WithLabelValues(status.NodeInfo.Network, status.NodeInfo.Moniker).Set(float64(status.SyncInfo.LatestBlockHeight))
	e.currentHeadGauge.Collect(ch)
	e.currentHeadGauge.Reset()

	// TODO:
	e.nodeMetadataGauge.WithLabelValues(status.NodeInfo.Network, status.NodeInfo.Moniker, "placeholder", "placeholder").Set(1)
	e.nodeMetadataGauge.Collect(ch)
	e.nodeMetadataGauge.Reset()

	return true
}

func (e *Exporter) collectAggregatorConfig(ch chan<- prometheus.Metric) bool {
	status, err := e.feedManager.TendermintClient.Status(context.Background())
	if err != nil {
		level.Error(e.logger).Log("msg", "Can't get the latest block height", "err", err)
		return false
	}
	for _, feed := range e.feedManager.Feeds {
		config, err := e.feedManager.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: feed.Aggregator,
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
			feed.Aggregator,
			feed.Status,
			ContractType,
			feed.Name,
			feed.Path,
			status.NodeInfo.Network,
			status.NodeInfo.Moniker,
			feed.Symbol,
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
			level.Info(e.logger).Log("msg", "Got New Round event", "round", round.RoundId, "Feed", round.Feed)
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
			level.Info(e.logger).Log("msg", "Got Submission Received event", "round id", round.RoundId, "submission", round.Submission.Key.Int64())
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
			level.Info(e.logger).Log("msg", "Got Answer Updated event", "round", update.RoundId, "Answer", update.Value.Key.Int64())
			e.answersEvents = append(e.answersEvents, update)
		}
		for _, confirm := range event.ConfirmAggregator {
			// This event indicates that the aggregator address for a feed has changed
			// So we need to unsubscribe from old aggregator's events
			err := e.feedManager.unsubscribe(e.feedManager.Feeds[confirm.Feed].Aggregator, e.logger)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not unsubscribe old aggregator", "err", err)
				continue
			}
			// Update the feed's aggregator
			feed := e.feedManager.Feeds[confirm.Feed]
			feed.Aggregator = confirm.NewAggregator
			e.feedManager.Feeds[confirm.Feed] = feed
			// And subscribe to new aggregator's events
			err = e.feedManager.subscribe(confirm.NewAggregator, e.msgCh, e.logger)
			if err != nil {
				level.Error(e.logger).Log("msg", "Could not unsubscribe old aggregator", "err", err)
				continue
			}
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

func (e *Exporter) pollChanges() {
	go e.feedManager.poll(e.msgCh, &e.mutex, e.logger)
}
