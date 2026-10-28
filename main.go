package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

//go:embed static
var staticFiles embed.FS

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	ttl, err := time.ParseDuration(env("CACHE_TTL", "10m"))
	if err != nil {
		log.Fatalf("bad CACHE_TTL: %v", err)
	}
	usdToman, _ := strconv.ParseFloat(env("USD_TOMAN", "0"), 64)

	client := &http.Client{Timeout: 12 * time.Second}
	store := NewStore([]Source{
		newBinanceSource(client, env("BINANCE_P2P_URL", "https://p2p.binance.com/bapi/c2c/v2/friendly/c2c/adv/search")),
		newMarketSource(client, env("MARKET_URL", "https://open.er-api.com/v6/latest/USD")),
		newCBASource(client, env("CBA_URL", "http://api.cba.am/exchangerates.asmx")),
		newSASSource(client, env("SAS_URL", "https://www.sas.am/")),
	}, ttl)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/rates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"fetched_at":        time.Now().UTC().Format(time.RFC3339),
			"sources":           store.Quotes(r.Context()),
			"usd_toman_default": usdToman,
		})
	})
	// Handy for curl tests: /api/convert?amd=130000&toman=165000
	mux.HandleFunc("GET /api/convert", func(w http.ResponseWriter, r *http.Request) {
		amd, err := strconv.ParseFloat(r.URL.Query().Get("amd"), 64)
		if err != nil || amd <= 0 {
			http.Error(w, "amd must be a positive number", http.StatusBadRequest)
			return
		}
		toman, _ := strconv.ParseFloat(r.URL.Query().Get("toman"), 64)
		if toman == 0 {
			toman = usdToman
		}
		writeJSON(w, map[string]any{
			"amd":       amd,
			"usd_toman": toman,
			"results":   Convert(amd, toman, store.Quotes(r.Context())),
		})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(static)))

	addr := ":" + env("PORT", "8080")
	log.Printf("listening on %s (cache ttl %s)", addr, ttl)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}
