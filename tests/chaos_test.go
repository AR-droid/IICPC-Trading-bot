package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

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
	Status    string  `json:"status"` // FILLED, PARTIALLY_FILLED, ACCEPTED, REJECTED
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

func getTargetURL() string {
	url := os.Getenv("TEST_TARGET_URL")
	if url == "" {
		url = "http://localhost:8080" // Defaults to template matching engine local port
	}
	return url
}

// 1. Boundary & Sanitization Validation Test
// Sends invalid, overflow, or negative pricing payloads to check if the submission crashes or handles them safely.
func TestBoundarySanitization(t *testing.T) {
	target := getTargetURL()

	invalidCases := []struct {
		name string
		req  OrderRequest
	}{
		{
			name: "Negative Price",
			req:  OrderRequest{Side: "BUY", Type: "LIMIT", Price: -100.50, Quantity: 10},
		},
		{
			name: "Negative Quantity",
			req:  OrderRequest{Side: "BUY", Type: "LIMIT", Price: 100.00, Quantity: -5},
		},
		{
			name: "Zero Quantity",
			req:  OrderRequest{Side: "SELL", Type: "LIMIT", Price: 100.00, Quantity: 0},
		},
		{
			name: "Market Order with zero quantity",
			req:  OrderRequest{Side: "BUY", Type: "MARKET", Price: 0, Quantity: 0},
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			bodyBytes, _ := json.Marshal(tc.req)
			resp, err := http.Post(target+"/order", "application/json", bytes.NewBuffer(bodyBytes))
			if err != nil {
				t.Fatalf("Failed to send request: %v", err)
			}
			defer resp.Body.Close()

			// Engine should either return a non-200 error status, or respond with a REJECTED flag
			if resp.StatusCode == http.StatusOK {
				var ordResp OrderResponse
				if err := json.NewDecoder(resp.Body).Decode(&ordResp); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if ordResp.Status != "REJECTED" {
					t.Errorf("Expected invalid order to be REJECTED, but got status: %s", ordResp.Status)
				}
			}
		})
	}
}

// 2. Price-Time Priority FIFO Rule Test
// Verifies that orders at the same price are matched strictly in chronological FIFO queue order.
func TestPriceTimePriority(t *testing.T) {
	target := getTargetURL()

	// Clear order book (can be simulated or just checked using a unique price point)
	testPrice := 876.50 // Unique price to prevent matching other orders

	// Order A (First Buy): 10 shares @ $876.50
	orderA := OrderRequest{Side: "BUY", Type: "LIMIT", Price: testPrice, Quantity: 10}
	bodyA, _ := json.Marshal(orderA)
	respA, err := http.Post(target+"/order", "application/json", bytes.NewBuffer(bodyA))
	if err != nil {
		t.Fatalf("Failed to submit Order A: %v", err)
	}
	var resA OrderResponse
	json.NewDecoder(respA.Body).Decode(&resA)
	respA.Body.Close()

	if resA.Status != "ACCEPTED" {
		t.Fatalf("Order A not accepted: %s", resA.Status)
	}

	// Wait briefly to ensure distinct timestamps
	time.Sleep(10 * time.Millisecond)

	// Order B (Second Buy): 10 shares @ $876.50
	orderB := OrderRequest{Side: "BUY", Type: "LIMIT", Price: testPrice, Quantity: 10}
	bodyB, _ := json.Marshal(orderB)
	respB, err := http.Post(target+"/order", "application/json", bytes.NewBuffer(bodyB))
	if err != nil {
		t.Fatalf("Failed to submit Order B: %v", err)
	}
	var resB OrderResponse
	json.NewDecoder(respB.Body).Decode(&resB)
	respB.Body.Close()

	if resB.Status != "ACCEPTED" {
		t.Fatalf("Order B not accepted: %s", resB.Status)
	}

	// Order C (Crossing Sell): Sell 15 shares @ $876.50
	// This should match ALL 10 shares of Order A, and only 5 shares of Order B
	orderC := OrderRequest{Side: "SELL", Type: "LIMIT", Price: testPrice, Quantity: 15}
	bodyC, _ := json.Marshal(orderC)
	respC, err := http.Post(target+"/order", "application/json", bytes.NewBuffer(bodyC))
	if err != nil {
		t.Fatalf("Failed to submit Order C: %v", err)
	}
	var resC OrderResponse
	json.NewDecoder(respC.Body).Decode(&resC)
	respC.Body.Close()

	if resC.Status != "FILLED" {
		t.Fatalf("Order C expected status FILLED, got %s", resC.Status)
	}

	// Validate matches
	if len(resC.Trades) != 2 {
		t.Fatalf("Expected Order C to yield 2 matches, got %d", len(resC.Trades))
	}

	// The first trade must match Order A (FIFO priority)
	firstTrade := resC.Trades[0]
	if firstTrade.MakerOrderID != resA.OrderID {
		t.Errorf("FIFO Violation: Expected first match to be Order A (%s), but was %s",
			resA.OrderID, firstTrade.MakerOrderID)
	}
	if firstTrade.Quantity != 10 {
		t.Errorf("Expected first trade quantity to be 10, got %d", firstTrade.Quantity)
	}

	// The second trade must match Order B
	secondTrade := resC.Trades[1]
	if secondTrade.MakerOrderID != resB.OrderID {
		t.Errorf("Expected second match to be Order B (%s), but was %s",
			resB.OrderID, secondTrade.MakerOrderID)
	}
	if secondTrade.Quantity != 5 {
		t.Errorf("Expected second trade quantity to be 5, got %d", secondTrade.Quantity)
	}

	// Clean up any remaining Bids (Order B should have 5 remaining)
	req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/order/%s", target, resB.OrderID), nil)
	cleanupResp, err := http.DefaultClient.Do(req)
	if err == nil {
		cleanupResp.Body.Close()
	}
}

// 3. Concurrency Thread-Safety Test
// Bombards the engine with multiple concurrent match requests to check for state corruption or double-fills.
func TestRaceConditionSafety(t *testing.T) {
	target := getTargetURL()

	// Step 1: Create a single Sell Limit for 10 shares
	sellPrice := 999.00
	sellOrder := OrderRequest{Side: "SELL", Type: "LIMIT", Price: sellPrice, Quantity: 10}
	sellBody, _ := json.Marshal(sellOrder)
	resp, err := http.Post(target+"/order", "application/json", bytes.NewBuffer(sellBody))
	if err != nil {
		t.Fatalf("Failed to initialize sell order: %v", err)
	}
	var sellRes OrderResponse
	json.NewDecoder(resp.Body).Decode(&sellRes)
	resp.Body.Close()

	if sellRes.Status != "ACCEPTED" {
		t.Fatalf("Sell order rejected: %s", sellRes.Status)
	}

	// Step 2: Fire 20 concurrent Buy Market orders for 1 share each.
	// If thread-safe, exactly 10 Buy orders will be filled, and the rest will be rejected/unmatched.
	// No double-matching must occur.
	concurrencyLimit := 20
	var wg sync.WaitGroup
	var filledCount int64
	var lock sync.Mutex
	var matchedTrades []Trade

	for i := 0; i < concurrencyLimit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			buyOrder := OrderRequest{Side: "BUY", Type: "MARKET", Quantity: 1}
			buyBody, _ := json.Marshal(buyOrder)
			
			// Fire request
			client := &http.Client{Timeout: 100 * time.Millisecond}
			resp, err := client.Post(target+"/order", "application/json", bytes.NewBuffer(buyBody))
			if err != nil {
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				var buyRes OrderResponse
				if err := json.NewDecoder(resp.Body).Decode(&buyRes); err == nil {
					if buyRes.Status == "FILLED" {
						lock.Lock()
						filledCount++
						matchedTrades = append(matchedTrades, buyRes.Trades...)
						lock.Unlock()
					}
				}
			}
		}()
	}

	wg.Wait()

	// Step 3: Audit results
	// If double-filling occurred, filledCount would exceed 10.
	// If locks were missed, we might also see a segmentation fault crash.
	if filledCount > 10 {
		t.Errorf("SECURITY VULNERABILITY: Race condition detected! Filled %d market buys against a sell order of only 10 shares", filledCount)
	}

	// Ensure no duplicate Trades IDs
	tradeIds := make(map[string]bool)
	for _, tr := range matchedTrades {
		if tradeIds[tr.TradeID] {
			t.Errorf("Duplicate Trade ID generated: %s", tr.TradeID)
		}
		tradeIds[tr.TradeID] = true
	}
}
