package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Side string
type OrderType string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"

	Limit  OrderType = "LIMIT"
	Market OrderType = "MARKET"
)

type Order struct {
	ID        string    `json:"id"`
	Side      Side      `json:"side"`
	Type      OrderType `json:"type"`
	Price     float64   `json:"price"`
	Quantity  int       `json:"quantity"`
	Timestamp int64     `json:"timestamp"`
}

type OrderBookEntry struct {
	Price    float64 `json:"price"`
	Quantity int     `json:"quantity"`
}

type OrderBookResponse struct {
	Bids []OrderBookEntry `json:"bids"`
	Asks []OrderBookEntry `json:"asks"`
}

type Trade struct {
	TradeID        string    `json:"trade_id"`
	MakerOrderID   string    `json:"maker_order_id"`
	TakerOrderID   string    `json:"taker_order_id"`
	Price          float64   `json:"price"`
	Quantity       int       `json:"quantity"`
	Timestamp      int64     `json:"timestamp"`
}

type OrderResponse struct {
	OrderID   string  `json:"order_id"`
	Status    string  `json:"status"` // FILLED, PARTIALLY_FILLED, ACCEPTED, REJECTED
	Remaining int     `json:"remaining"`
	Trades    []Trade `json:"trades,omitempty"`
}

type MatchingEngine struct {
	mu        sync.RWMutex
	bids      []*Order
	asks      []*Order
	orders    map[string]*Order
	nextOrderID int
	nextTradeID int
}

func NewMatchingEngine() *MatchingEngine {
	return &MatchingEngine{
		bids:   make([]*Order, 0),
		asks:   make([]*Order, 0),
		orders: make(map[string]*Order),
	}
}

func (me *MatchingEngine) generateOrderID() string {
	me.nextOrderID++
	return fmt.Sprintf("ord_%d", me.nextOrderID)
}

func (me *MatchingEngine) generateTradeID() string {
	me.nextTradeID++
	return fmt.Sprintf("trd_%d", me.nextTradeID)
}

func (me *MatchingEngine) SubmitOrder(side Side, orderType OrderType, price float64, qty int) OrderResponse {
	me.mu.Lock()
	defer me.mu.Unlock()

	if qty <= 0 {
		return OrderResponse{Status: "REJECTED"}
	}
	if orderType == Limit && price <= 0 {
		return OrderResponse{Status: "REJECTED"}
	}

	order := &Order{
		ID:        me.generateOrderID(),
		Side:      side,
		Type:      orderType,
		Price:     price,
		Quantity:  qty,
		Timestamp: time.Now().UnixNano(),
	}

	me.orders[order.ID] = order

	var trades []Trade
	remaining := qty

	if side == Buy {
		// Match against asks
		i := 0
		for i < len(me.asks) && remaining > 0 {
			ask := me.asks[i]
			if orderType == Limit && ask.Price > price {
				break // Cannot match further
			}

			// Determine match quantity
			matchQty := remaining
			if ask.Quantity < matchQty {
				matchQty = ask.Quantity
			}

			// Generate trade
			trade := Trade{
				TradeID:      me.generateTradeID(),
				MakerOrderID: ask.ID,
				TakerOrderID: order.ID,
				Price:        ask.Price,
				Quantity:     matchQty,
				Timestamp:    time.Now().UnixNano(),
			}
			trades = append(trades, trade)

			remaining -= matchQty
			ask.Quantity -= matchQty
			if ask.Quantity == 0 {
				// Remove ask
				me.asks = append(me.asks[:i], me.asks[i+1:]...)
				delete(me.orders, ask.ID)
			} else {
				i++
			}
		}

		if remaining > 0 && orderType == Limit {
			order.Quantity = remaining
			me.bids = append(me.bids, order)
			// Sort bids: descending by price, then ascending by timestamp
			sort.Slice(me.bids, func(i, j int) bool {
				if me.bids[i].Price == me.bids[j].Price {
					return me.bids[i].Timestamp < me.bids[j].Timestamp
				}
				return me.bids[i].Price > me.bids[j].Price
			})
		}
	} else {
		// Sell order: match against bids
		i := 0
		for i < len(me.bids) && remaining > 0 {
			bid := me.bids[i]
			if orderType == Limit && bid.Price < price {
				break // Cannot match further
			}

			// Determine match quantity
			matchQty := remaining
			if bid.Quantity < matchQty {
				matchQty = bid.Quantity
			}

			// Generate trade
			trade := Trade{
				TradeID:      me.generateTradeID(),
				MakerOrderID: bid.ID,
				TakerOrderID: order.ID,
				Price:        bid.Price,
				Quantity:     matchQty,
				Timestamp:    time.Now().UnixNano(),
			}
			trades = append(trades, trade)

			remaining -= matchQty
			bid.Quantity -= matchQty
			if bid.Quantity == 0 {
				// Remove bid
				me.bids = append(me.bids[:i], me.bids[i+1:]...)
				delete(me.orders, bid.ID)
			} else {
				i++
			}
		}

		if remaining > 0 && orderType == Limit {
			order.Quantity = remaining
			me.asks = append(me.asks, order)
			// Sort asks: ascending by price, then ascending by timestamp
			sort.Slice(me.asks, func(i, j int) bool {
				if me.asks[i].Price == me.asks[j].Price {
					return me.asks[i].Timestamp < me.asks[j].Timestamp
				}
				return me.asks[i].Price < me.asks[j].Price
			})
		}
	}

	status := "FILLED"
	if remaining > 0 {
		if remaining == qty {
			status = "ACCEPTED"
		} else {
			status = "PARTIALLY_FILLED"
		}
	}

	if status == "FILLED" {
		delete(me.orders, order.ID)
	}

	return OrderResponse{
		OrderID:   order.ID,
		Status:    status,
		Remaining: remaining,
		Trades:    trades,
	}
}

func (me *MatchingEngine) CancelOrder(orderID string) bool {
	me.mu.Lock()
	defer me.mu.Unlock()

	order, exists := me.orders[orderID]
	if !exists {
		return false
	}

	delete(me.orders, orderID)

	if order.Side == Buy {
		for i, o := range me.bids {
			if o.ID == orderID {
				me.bids = append(me.bids[:i], me.bids[i+1:]...)
				return true
			}
		}
	} else {
		for i, o := range me.asks {
			if o.ID == orderID {
				me.asks = append(me.asks[:i], me.asks[i+1:]...)
				return true
			}
		}
	}

	return false
}

func (me *MatchingEngine) GetOrderBook() OrderBookResponse {
	me.mu.RLock()
	defer me.mu.RUnlock()

	// Aggregate levels
	bidsMap := make(map[float64]int)
	for _, o := range me.bids {
		bidsMap[o.Price] += o.Quantity
	}
	var bids []OrderBookEntry
	for p, q := range bidsMap {
		bids = append(bids, OrderBookEntry{Price: p, Quantity: q})
	}
	sort.Slice(bids, func(i, j int) bool {
		return bids[i].Price > bids[j].Price
	})

	asksMap := make(map[float64]int)
	for _, o := range me.asks {
		asksMap[o.Price] += o.Quantity
	}
	var asks []OrderBookEntry
	for p, q := range asksMap {
		asks = append(asks, OrderBookEntry{Price: p, Quantity: q})
	}
	sort.Slice(asks, func(i, j int) bool {
		return asks[i].Price < asks[j].Price
	})

	return OrderBookResponse{Bids: bids, Asks: asks}
}

func main() {
	engine := NewMatchingEngine()

	// API endpoints using native Go HTTP multiplexer
	http.HandleFunc("POST /order", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Side     Side      `json:"side"`
			Type     OrderType `json:"type"`
			Price    float64   `json:"price"`
			Quantity int       `json:"quantity"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		resp := engine.SubmitOrder(req.Side, req.Type, req.Price, req.Quantity)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("DELETE /order/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "missing order id", http.StatusBadRequest)
			return
		}

		success := engine.CancelOrder(id)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": success})
	})

	http.HandleFunc("GET /orderbook", func(w http.ResponseWriter, r *http.Request) {
		resp := engine.GetOrderBook()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	})

	port := 8080
	log.Printf("Matching Engine Template running on port %d...", port)
	if err := http.ListenAndServe(":"+strconv.Itoa(port), nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
