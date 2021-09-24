package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-cmp/cmp"
	"github.com/hackbg/terra-chainlink-exporter/types"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
	"google.golang.org/grpc"
)

type FeedManager struct {
	WasmClient       wasmTypes.QueryClient
	TendermintClient *tmrpc.HTTP
	Feeds            map[string]types.Feed
}

func getConfig(feeds map[string]types.Feed) error {
	var config []types.Feed
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

func NewManager(tendermint_url, terra_rpc string) (*FeedManager, error) {
	var feeds = make(map[string]types.Feed)
	grpcConn, err := grpc.Dial(
		terra_rpc,
		grpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	client, err := tmrpc.New(fmt.Sprintf("http://%s", TENDERMINT_URL), "/websocket")

	if err != nil {
		return nil, err
	}
	err = client.Start()
	if err != nil {
		return nil, err
	}

	err = getConfig(feeds)

	if err != nil {
		return nil, err
	}

	return &FeedManager{
		WasmClient:       wasmTypes.NewQueryClient(grpcConn),
		TendermintClient: client,
		Feeds:            feeds,
	}, nil
}

func (fm *FeedManager) subscribe(address string, msgs chan types.Message, logger log.Logger) (err error) {
	level.Info(logger).Log("msg", "Subscribing to feed", "address", address)
	query := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", address)
	out, err := fm.TendermintClient.Subscribe(context.Background(), "subscribe", query)

	if err != nil {
		return err
	}

	go func() {
		for {
			resp, ok := <-out
			if !ok {
				return
			}
			msg := types.Message{Event: resp, Address: address}
			msgs <- msg
		}
	}()

	return nil
}

func (fm *FeedManager) unsubscribe(address string, logger log.Logger) error {
	level.Info(logger).Log("msg", "Unsubscribing from feed", "address", address)
	query := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", address)
	return fm.TendermintClient.Unsubscribe(context.Background(), "unsubscribe", query)
}

func (fm *FeedManager) initializeFeeds(ch chan types.Message, logger log.Logger) error {
	for _, feed := range fm.Feeds {
		aggregator, err := fm.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: feed.ContractAddress,
				QueryMsg:        []byte(`{"get_aggregator": {}}`),
			},
		)

		if err != nil {
			return err
		}

		var aggregatorAddress string
		err = json.Unmarshal(aggregator.QueryResult, &aggregatorAddress)

		if err != nil {
			return err
		}

		// update the feed
		feed.Aggregator = aggregatorAddress
		fm.Feeds[feed.ContractAddress] = feed

		err = fm.subscribe(aggregatorAddress, ch, logger)

		if err != nil {
			level.Error(logger).Log("msg", "Can't subscribe to address", "err", err)
			return err
		}

	}
	return nil
}

func (fm *FeedManager) poll(msgs chan types.Message, mu *sync.Mutex, logger log.Logger) {
	var newFeeds = make(map[string]types.Feed)
	ticker := time.NewTicker(5 * time.Second)

	for range ticker.C {
		err := getConfig(newFeeds)
		if err != nil {
			continue
		}
		for _, feed := range newFeeds {
			mu.Lock()
			fm.updateFeed(feed, msgs, logger)
			mu.Unlock()
		}
	}
}

func (fm *FeedManager) updateFeed(feed types.Feed, msgs chan types.Message, logger log.Logger) {
	_, present := fm.Feeds[feed.ContractAddress]
	// if the proxy is not present in the list of feeds, create a new feed and subscribe to events
	if !present {
		//fm.Feeds[feed.ContractAddress] = feed
		aggregator, err := fm.WasmClient.ContractStore(
			context.Background(),
			&wasmTypes.QueryContractStoreRequest{
				ContractAddress: feed.ContractAddress,
				QueryMsg:        []byte(`{"get_aggregator": {}}`),
			},
		)

		if err != nil {
			return
		}

		var aggregatorAddress string
		err = json.Unmarshal(aggregator.QueryResult, &aggregatorAddress)

		if err != nil {
			return
		}

		// Add new feed
		feed.Aggregator = aggregatorAddress
		fm.Feeds[feed.ContractAddress] = feed

		err = fm.subscribe(aggregatorAddress, msgs, logger)
		if err != nil {
			level.Error(logger).Log("msg", "Could not subscribe to feed", "err", err)
			return
		}
	}

	// Check if aggregator address has changed
	aggregator, err := fm.WasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: feed.ContractAddress,
			QueryMsg:        []byte(`{"get_aggregator": {}}`),
		},
	)

	if err != nil {
		return
	}

	var aggregatorAddress string
	err = json.Unmarshal(aggregator.QueryResult, &aggregatorAddress)

	if err != nil {
		return
	}
	changed := fm.Feeds[feed.ContractAddress].Aggregator != aggregatorAddress
	feed.Aggregator = aggregatorAddress

	// Check if any of feed configurations have changed
	res := cmp.Equal(fm.Feeds[feed.ContractAddress], feed)

	// if either changed we need to update and resubscribe
	if !res {
		level.Info(logger).Log("msg", "Feed configuration has changed", "changed", res)
	}
	// if aggregator changed resub
	if changed {
		err = fm.unsubscribe(fm.Feeds[feed.ContractAddress].Aggregator, logger)
		if err != nil {
			level.Error(logger).Log("msg", "Could not unsubscribe from feed", "err", err)
			return
		}
		fm.Feeds[feed.ContractAddress] = feed
		err = fm.subscribe(aggregatorAddress, msgs, logger)

		if err != nil {
			level.Error(logger).Log("msg", "Could not resubscribe to feed", "err", err)
			return
		}
	}
}
