# Prometheus exporter for Terra contracts

## Configuration
### Environment Variables
| Key                        | Description                                                                                | Example                                                            |
| -------------------------- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------------ |
| `TERRA_RPC`                | The RPC address of the Terra node                                                          | `terrad:9090`                                                      |
| `TENDERMINT_URL`           | Tendermint Connection URL                                                                  | `http://terrad:26657`                                              |
| `KAFKA_SERVER`             | Kafka Connection URL                                                                       | `kafka:9092`                                                       |
| `TOPIC`                    | Kafka Topic that the events are pushed to                                                  | `Terra`                                                            |
| `FEED_CONFIG_URL`          | URL to poll for feed configs                                                               | `http://localhost:4000/feeds`                                      |
| `NODE_CONFIG_URL`          | URL to poll for feed configs                                                               | `http://localhost:4000/nodes`                                      |
| `FEED_POLLING_INTERVAL`    | Polling interval for feed configurations                                                   | `10s`                                                              |
| `NODE_POLLING_INTERVAL`    | Polling interval for node configurations                                                   | `10s`                                                              |

---
- [x] Counters
  - [x] flux_monitor_answers_total
  - [x] flux_monitor_submissions_received_total
  - [x] flux_monitor_rounds
- [ ] Gauges
  - [x] flux_monitor_submission_received_values
  - [x] node_metadata
  - [x] feed_contract_metadata
  - [x] flux_monitor_answers
  - [x] head_tracker_current_head
  - [x] flux_monitor_latest_round_responses
  - [ ] flux_monitor_round_age
- [ ] Events
  - [x] NewRound
  - [x] SubmissionReceived
  - [x] AnswerUpdated
  - [ ] BalanceUpdated
  - [ ] TXFailed
  - [ ] TXSucceeded