package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

type Job struct {
	TeamName  string `json:"team_name"`
	TargetURL string `json:"target_url"`
	BotCount  int    `json:"bot_count"`
	Duration  int    `json:"duration"` // in seconds
}

type OrderRequest struct {
	Side     string  `json:"side"`
	Type     string  `json:"type"`
	Price    float64 `json:"price"`
	Quantity int     `json:"quantity"`
}

type Trade struct {
	TradeID      string  `json:"trade_id"`
	MakerOrderID string  `json:"maker_order_id"`
	TakerOrderID string  `json:"taker_order_id"`
	Price        float64 `json:"price"`
	Quantity     int     `json:"quantity"`
}

type OrderResponse struct {
	OrderID   string  `json:"order_id"`
	Status    string  `json:"status"`
	Remaining int     `json:"remaining"`
	Trades    []Trade `json:"trades,omitempty"`
}

type OrderBookEntry struct {
	Price    float64 `json:"price"`
	Quantity int     `json:"quantity"`
}

type OrderBookResponse struct {
	Bids []OrderBookEntry `json:"bids"`
	Asks []OrderBookEntry `json:"asks"`
}

type WindowMetrics struct {
	Timestamp         int64   `json:"timestamp"`
	TPS               int64   `json:"tps"`
	P50               float64 `json:"p50"` // ms
	P90               float64 `json:"p90"` // ms
	P99               float64 `json:"p99"` // ms
	ErrorRate         float64 `json:"error_rate"`
	CorrectnessErrors int64   `json:"correctness_errors"`
	ActiveBots        int     `json:"active_bots"`
}

type FinalScore struct {
	TeamName          string  `json:"team_name"`
	AverageTPS        float64 `json:"average_tps"`
	P99Latency        float64 `json:"p99_latency"`
	SuccessRate       float64 `json:"success_rate"`
	CorrectnessErrors int64   `json:"correctness_errors"`
	Score             float64 `json:"score"`
}

var (
	rdb *redis.Client
	ctx = context.Background()
)

func main() {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379/0"
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("Error parsing REDIS_URL: %v", err)
	}

	rdb = redis.NewClient(opt)
	log.Printf("Load Generator connected to Redis at %s", redisURL)

	// Main job worker loop
	for {
		log.Println("Waiting for load test jobs in queue 'test_jobs'...")
		res, err := rdb.BRPop(ctx, 0, "test_jobs").Result()
		if err != nil {
			log.Printf("Error popping from queue: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		// res[0] is the queue name, res[1] is the value
		var job Job
		if err := json.Unmarshal([]byte(res[1]), &job); err != nil {
			log.Printf("Failed to decode job message: %v", err)
			continue
		}

		log.Printf("Starting stress test for team '%s' on target '%s' with %d bots for %ds",
			job.TeamName, job.TargetURL, job.BotCount, job.Duration)

		runLoadTest(job)
	}
}

func runLoadTest(job Job) {
	// Publish started event
	publishEvent("started", map[string]interface{}{
		"team_name":  job.TeamName,
		"duration":   job.Duration,
		"bot_count":  job.BotCount,
		"target_url": job.TargetURL,
	})

	httpClient := &http.Client{
		Timeout: 200 * time.Millisecond, // Strict timeout for trading latency
	}

	var totalReqs int64
	var failedReqs int64
	var correctnessErrors int64

	// Channel to collect latencies for the active window
	latencyChan := make(chan float64, 100000)

	// Keep track of placed orders for cancels
	var orderLock sync.Mutex
	var activeOrderIDs []string

	stopChan := make(chan struct{})
	var wg sync.WaitGroup

	// Spawn bots
	for i := 0; i < job.BotCount; i++ {
		wg.Add(1)
		go func(botID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(botID)))

			for {
				select {
				case <-stopChan:
					return
				default:
					action := rng.Float64()
					start := time.Now()

					if action < 0.15 && len(activeOrderIDs) > 0 {
						// Cancel action
						orderLock.Lock()
						var targetID string
						if len(activeOrderIDs) > 0 {
							idx := rng.Intn(len(activeOrderIDs))
							targetID = activeOrderIDs[idx]
							// Remove from active list
							activeOrderIDs = append(activeOrderIDs[:idx], activeOrderIDs[idx+1:]...)
						}
						orderLock.Unlock()

						if targetID != "" {
							req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/order/%s", job.TargetURL, targetID), nil)
							atomic.AddInt64(&totalReqs, 1)
							resp, err := httpClient.Do(req)
							latency := time.Since(start).Seconds() * 1000.0 // ms
							select {
							case latencyChan <- latency:
							default:
							}

							if err != nil {
								atomic.AddInt64(&failedReqs, 1)
							} else {
								resp.Body.Close()
								if resp.StatusCode != http.StatusOK {
									atomic.AddInt64(&failedReqs, 1)
								}
							}
						}
					} else {
						// Place order action
						side := "BUY"
						if rng.Float64() > 0.5 {
							side = "SELL"
						}

						orderType := "LIMIT"
						if rng.Float64() < 0.2 {
							orderType = "MARKET"
						}

						// Mid price around 100
						price := 100.0 + (rng.Float64()*10.0 - 5.0)
						price = math.Round(price*100) / 100 // 2 decimals
						qty := rng.Intn(50) + 1

						orderReq := OrderRequest{
							Side:     side,
							Type:     orderType,
							Price:    price,
							Quantity: qty,
						}

						bodyBytes, _ := json.Marshal(orderReq)
						atomic.AddInt64(&totalReqs, 1)
						resp, err := httpClient.Post(job.TargetURL+"/order", "application/json", bytes.NewBuffer(bodyBytes))
						latency := time.Since(start).Seconds() * 1000.0 // ms
						select {
						case latencyChan <- latency:
						default:
						}

						if err != nil {
							atomic.AddInt64(&failedReqs, 1)
						} else {
							var ordResp OrderResponse
							errDec := json.NewDecoder(resp.Body).Decode(&ordResp)
							resp.Body.Close()

							if resp.StatusCode != http.StatusOK || errDec != nil {
								atomic.AddInt64(&failedReqs, 1)
							} else {
								// Correctness Validation 1: Check match logic
								if ordResp.Status == "FILLED" || ordResp.Status == "PARTIALLY_FILLED" {
									for _, tr := range ordResp.Trades {
										// Check trade price bounds
										if orderType == "LIMIT" {
											if side == "BUY" && tr.Price > price {
												atomic.AddInt64(&correctnessErrors, 1)
												log.Printf("[CORRECTNESS ERROR] Trade price %.2f higher than Buy Limit price %.2f", tr.Price, price)
											}
											if side == "SELL" && tr.Price < price {
												atomic.AddInt64(&correctnessErrors, 1)
												log.Printf("[CORRECTNESS ERROR] Trade price %.2f lower than Sell Limit price %.2f", tr.Price, price)
											}
										}
									}
								}

								// Store limit order ID for potential future cancel
								if orderType == "LIMIT" && ordResp.Status == "ACCEPTED" {
									orderLock.Lock()
									activeOrderIDs = append(activeOrderIDs, ordResp.OrderID)
									// Limit active IDs to prevent infinite memory growth
									if len(activeOrderIDs) > 500 {
										activeOrderIDs = activeOrderIDs[1:]
									}
									orderLock.Unlock()
								}
							}
						}
					}

					// Rest to simulate dynamic speed and throughput
					time.Sleep(time.Duration(rng.Intn(10)+1) * time.Millisecond)
				}
			}
		}(i)
	}

	// Channel to aggregate windows
	var history []WindowMetrics
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Correctness orderbook checker timer
	bookCheckTicker := time.NewTicker(2 * time.Second)
	defer bookCheckTicker.Stop()

	doneChan := time.After(time.Duration(job.Duration) * time.Second)

	var lastTotalReqs int64
	var lastFailedReqs int64

	for {
		select {
		case <-doneChan:
			// Stop all bots
			close(stopChan)
			wg.Wait()

			// Finalize scoring
			calculateAndSaveFinalScore(job, history, atomic.LoadInt64(&correctnessErrors))
			return

		case <-bookCheckTicker.C:
			// Perform correctness check on the Orderbook spread
			go func() {
				resp, err := httpClient.Get(job.TargetURL + "/orderbook")
				if err != nil {
					return
				}
				defer resp.Body.Close()

				var book OrderBookResponse
				if err := json.NewDecoder(resp.Body).Decode(&book); err == nil {
					if len(book.Bids) > 0 && len(book.Asks) > 0 {
						bestBid := book.Bids[0].Price
						bestAsk := book.Asks[0].Price
						// Bids must always be less than asks. If best bid >= best ask, order book is crossed!
						if bestBid >= bestAsk {
							atomic.AddInt64(&correctnessErrors, 1)
							log.Printf("[CORRECTNESS ERROR] Crossed orderbook spread! Best Bid: %.2f, Best Ask: %.2f", bestBid, bestAsk)
						}
					}
				}
			}()

		case <-ticker.C:
			// Aggregator logic (once a second)
			currTotalReqs := atomic.LoadInt64(&totalReqs)
			currFailedReqs := atomic.LoadInt64(&failedReqs)

			windowReqs := currTotalReqs - lastTotalReqs
			windowFails := currFailedReqs - lastFailedReqs

			lastTotalReqs = currTotalReqs
			lastFailedReqs = currFailedReqs

			// Consume latencies
			var latencies []float64
			n := len(latencyChan)
			for i := 0; i < n; i++ {
				latencies = append(latencies, <-latencyChan)
			}

			// Sort to calculate percentiles
			var p50, p90, p99 float64
			if len(latencies) > 0 {
				sort.Float64s(latencies)
				p50 = percentile(latencies, 50)
				p90 = percentile(latencies, 90)
				p99 = percentile(latencies, 99)
			}

			errRate := 0.0
			if windowReqs > 0 {
				errRate = float64(windowFails) / float64(windowReqs)
			}

			windowMetrics := WindowMetrics{
				Timestamp:         time.Now().Unix(),
				TPS:               windowReqs,
				P50:               p50,
				P90:               p90,
				P99:               p99,
				ErrorRate:         errRate,
				CorrectnessErrors: atomic.LoadInt64(&correctnessErrors),
				ActiveBots:        job.BotCount,
			}

			history = append(history, windowMetrics)

			// Store in Redis
			metricJSON, _ := json.Marshal(windowMetrics)
			rdb.LPush(ctx, fmt.Sprintf("metrics:%s", job.TeamName), metricJSON)
			// Limit history to last 300 entries in Redis
			rdb.LTrim(ctx, fmt.Sprintf("metrics:%s", job.TeamName), 0, 300)

			// Publish to live websocket pub/sub
			rdb.Publish(ctx, "live_metrics", metricJSON)
			log.Printf("[METRICS] TPS: %d | p50: %.2fms | p99: %.2fms | Errors: %d | Correctness: %d",
				windowMetrics.TPS, windowMetrics.P50, windowMetrics.P99, windowFails, windowMetrics.CorrectnessErrors)
		}
	}
}

func calculateAndSaveFinalScore(job Job, history []WindowMetrics, totalCorrectnessErrors int64) {
	if len(history) == 0 {
		publishEvent("finished", map[string]interface{}{
			"team_name": job.TeamName,
			"error":     "No telemetry collected",
		})
		return
	}

	var sumTPS int64
	var sumP99 float64
	var sumErrRate float64

	for _, w := range history {
		sumTPS += w.TPS
		sumP99 += w.P99
		sumErrRate += w.ErrorRate
	}

	n := float64(len(history))
	avgTPS := float64(sumTPS) / n
	avgP99 := sumP99 / n
	avgErrRate := sumErrRate / n
	successRate := 1.0 - avgErrRate

	// Score Formula: Score = AvgTPS - (P99_ms * 0.5) - (AvgErrRate * 500) - (CorrectnessErrors * 1000)
	// Make sure score doesn't fall below 0
	score := avgTPS - (avgP99 * 0.5) - (avgErrRate * 500.0) - (float64(totalCorrectnessErrors) * 1000.0)
	if score < 0 {
		score = 0
	}

	finalScore := FinalScore{
		TeamName:          job.TeamName,
		AverageTPS:        avgTPS,
		P99Latency:        avgP99,
		SuccessRate:       successRate,
		CorrectnessErrors: totalCorrectnessErrors,
		Score:             score,
	}

	// Save final stats to Redis
	finalJSON, _ := json.Marshal(finalScore)
	rdb.HSet(ctx, "contestant_results", job.TeamName, finalJSON)

	// Add to leaderboard sorted set
	rdb.ZAdd(ctx, "leaderboard", redis.Z{
		Score:  score,
		Member: job.TeamName,
	})

	publishEvent("finished", map[string]interface{}{
		"team_name": job.TeamName,
		"results":   finalScore,
	})

	log.Printf("[FINISHED] Team %s Final Score: %.2f | Avg TPS: %.2f | p99: %.2fms | Correctness Errors: %d",
		job.TeamName, score, avgTPS, avgP99, totalCorrectnessErrors)
}

func percentile(sorted []float64, pct float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := (pct / 100.0) * float64(len(sorted)-1)
	low := math.Floor(idx)
	high := math.Ceil(idx)
	if low == high {
		return sorted[int(low)]
	}
	// Linear interpolation
	return sorted[int(low)] + (idx-low)*(sorted[int(high)]-sorted[int(low)])
}

func publishEvent(event string, data map[string]interface{}) {
	payload := map[string]interface{}{
		"event": event,
		"data":  data,
	}
	jsonBytes, _ := json.Marshal(payload)
	rdb.Publish(ctx, "test_events", jsonBytes)
}
