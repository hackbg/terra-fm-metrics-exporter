package manager

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/hackbg/terra-chainlink-exporter/types"
	tmrpc "github.com/tendermint/tendermint/rpc/client/http"
	wasmTypes "github.com/terra-money/core/x/wasm/types"
)

type FeedManager struct {
	TendermintClient *tmrpc.HTTP
	Feed             types.FeedConfig
}

func NewManager(feed types.FeedConfig, client *tmrpc.HTTP, logger log.Logger) *FeedManager {
	return &FeedManager{
		TendermintClient: client,
		Feed:             feed,
	}
}

func (fm *FeedManager) Subscribe(msgs chan types.Message, logger log.Logger, address string) (err error) {
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
			msg := types.Message{Event: resp, Address: fm.Feed.Aggregator}
			msgs <- msg
		}
	}()

	return nil
}

func (fm *FeedManager) Unsubscribe(logger log.Logger) error {
	level.Info(logger).Log("msg", "Unsubscribing from feed", "address", fm.Feed.Aggregator)
	query := fmt.Sprintf("tm.event='Tx' AND execute_contract.contract_address='%s'", fm.Feed.Aggregator)
	return fm.TendermintClient.Unsubscribe(context.Background(), "unsubscribe", query)
}

func (fm *FeedManager) GetAggregator(proxyAddress string, wasmClient wasmTypes.QueryClient) (aggregator *string, err error) {
	res, err := wasmClient.ContractStore(
		context.Background(),
		&wasmTypes.QueryContractStoreRequest{
			ContractAddress: proxyAddress,
			QueryMsg:        []byte(`{"get_aggregator": {}}`),
		},
	)

	if err != nil {
		return nil, err
	}

	var aggregatorAddress string
	err = json.Unmarshal(res.QueryResult, &aggregatorAddress)

	if err != nil {
		return nil, err
	}

	return &aggregatorAddress, nil
}

func (fm *FeedManager) UpdateFeed(msgs chan types.Message, wasmClient wasmTypes.QueryClient, logger log.Logger, newConfig types.FeedConfig) {
	aggregator, err := fm.GetAggregator(fm.Feed.ContractAddress, wasmClient)
	if err != nil {
		level.Error(logger).Log("msg", "Could not get the aggregator address", "err", err)
		return
	}
	// Check if any of feed configurations have changed ignoring the Aggregator field
	res := cmp.Equal(fm.Feed, newConfig, cmpopts.IgnoreFields(fm.Feed, "Aggregator"))
	// if either changed we need to update and resubscribe
	if !res {
		fmt.Println("RES is false")
		level.Info(logger).Log("msg", "Feed configuration has changed")
		newConfig.Aggregator = *aggregator
		fm.Feed = newConfig
	}
}
