package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// maxBody caps how much of any upstream response we read.
const maxBody = 5 << 20

// Quote is one source's USD→AMD rate. Sources either publish a single
// mid-market rate (Mid) or a buy/sell pair (Buy/Sell), never both.
type Quote struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Mid   *float64 `json:"mid,omitempty"`
	Buy   *float64 `json:"buy,omitempty"`  // what the exchanger pays you per 1 USD
	Sell  *float64 `json:"sell,omitempty"` // what the exchanger charges you per 1 USD
	AsOf  string   `json:"as_of,omitempty"`
	Error string   `json:"error,omitempty"`
	Stale bool     `json:"stale,omitempty"`
}

// Source knows how to fetch one Quote.
type Source struct {
	ID    string
	Name  string
	Fetch func(ctx context.Context) (Quote, error)
}

func get(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(client, req)
}

func do(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func f64(v float64) *float64 { return &v }

// ---------------------------------------------------------------------------
// Market rate: open.er-api.com (free, no key, updates once a day).
// Attribution to exchangerate-api.com is required; the UI footer links to it.
// ---------------------------------------------------------------------------

func newMarketSource(client *http.Client, url string) Source {
	s := Source{ID: "market", Name: "Market rate"}
	s.Fetch = func(ctx context.Context) (Quote, error) {
		body, err := get(ctx, client, url, nil)
		if err != nil {
			return Quote{}, err
		}
		return parseMarket(body, s)
	}
	return s
}

func parseMarket(body []byte, s Source) (Quote, error) {
	var r struct {
		Result  string             `json:"result"`
		Rates   map[string]float64 `json:"rates"`
		Updated string             `json:"time_last_update_utc"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Quote{}, fmt.Errorf("bad JSON: %w", err)
	}
	if r.Result != "success" {
		return Quote{}, errors.New("API returned result=" + r.Result)
	}
	amd := r.Rates["AMD"]
	if amd <= 0 {
		return Quote{}, errors.New("AMD missing from response")
	}
	return Quote{ID: s.ID, Name: s.Name, Mid: f64(amd), AsOf: r.Updated}, nil
}

// ---------------------------------------------------------------------------
// Central Bank of Armenia: official daily reference rate (SOAP service).
// ---------------------------------------------------------------------------

const cbaEnvelope = `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body><ExchangeRatesLatest xmlns="http://www.cba.am/"/></soap:Body>
</soap:Envelope>`

func newCBASource(client *http.Client, url string) Source {
	s := Source{ID: "cba", Name: "Central Bank of Armenia"}
	s.Fetch = func(ctx context.Context) (Quote, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(cbaEnvelope))
		if err != nil {
			return Quote{}, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		req.Header.Set("SOAPAction", "http://www.cba.am/ExchangeRatesLatest")
		body, err := do(client, req)
		if err != nil {
			return Quote{}, err
		}
		return parseCBA(body, s)
	}
	return s
}

// Element names are matched without namespaces, so a namespace change on the
// CBA side won't break parsing.
type cbaResponse struct {
	Fault *struct {
		String string `xml:"faultstring"`
	} `xml:"Body>Fault"`
	Date  string `xml:"Body>ExchangeRatesLatestResponse>ExchangeRatesLatestResult>CurrentDate"`
	Rates []struct {
		ISO    string  `xml:"ISO"`
		Amount float64 `xml:"Amount"`
		Rate   float64 `xml:"Rate"`
	} `xml:"Body>ExchangeRatesLatestResponse>ExchangeRatesLatestResult>Rates>ExchangeRate"`
}

func parseCBA(body []byte, s Source) (Quote, error) {
	var r cbaResponse
	if err := xml.Unmarshal(body, &r); err != nil {
		return Quote{}, fmt.Errorf("bad XML: %w", err)
	}
	if r.Fault != nil {
		return Quote{}, errors.New("SOAP fault: " + r.Fault.String)
	}
	for _, x := range r.Rates {
		if x.ISO == "USD" && x.Rate > 0 {
			amount := x.Amount
			if amount <= 0 {
				amount = 1 // CBA quotes some currencies per 10/100 units
			}
			return Quote{ID: s.ID, Name: s.Name, Mid: f64(x.Rate / amount), AsOf: r.Date}, nil
		}
	}
	return Quote{}, errors.New("USD not found in response")
}

// ---------------------------------------------------------------------------
// SAS supermarket exchange table. The table is rendered into the home page
// HTML, so there is no API to call: we strip the markup and read the row
// "USD <buy> <sell>".
// ---------------------------------------------------------------------------

var (
	reScript = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace  = regexp.MustCompile(`\s+`)
	reUSDRow = regexp.MustCompile(`\bUSD\s+(\d+(?:[.,]\d+)?)\s+(\d+(?:[.,]\d+)?)`)
	reSASDay = regexp.MustCompile(`Թարմացվել է[^\d]{0,5}(\d{2}\.\d{2}\.\d{4})`)
)

func newSASSource(client *http.Client, url string) Source {
	s := Source{ID: "sas", Name: "SAS Supermarket"}
	s.Fetch = func(ctx context.Context) (Quote, error) {
		body, err := get(ctx, client, url, map[string]string{"Accept-Language": "hy,en;q=0.8"})
		if err != nil {
			return Quote{}, err
		}
		return parseSAS(body, s)
	}
	return s
}

func parseSAS(body []byte, s Source) (Quote, error) {
	text := reScript.ReplaceAllString(string(body), " ")
	text = reTag.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	text = reSpace.ReplaceAllString(text, " ")

	m := reUSDRow.FindStringSubmatch(text)
	if m == nil {
		return Quote{}, errors.New("USD row not found (page layout may have changed)")
	}
	buy, err1 := parseDecimal(m[1])
	sell, err2 := parseDecimal(m[2])
	if err1 != nil || err2 != nil || buy <= 0 || sell <= 0 {
		return Quote{}, fmt.Errorf("could not read rates %q / %q", m[1], m[2])
	}
	q := Quote{ID: s.ID, Name: s.Name, Buy: f64(buy), Sell: f64(sell)}
	if d := reSASDay.FindStringSubmatch(text); d != nil {
		q.AsOf = d[1]
	}
	return q, nil
}

func parseDecimal(s string) (float64, error) {
	return strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
}
