package main

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseMarket(t *testing.T) {
	body := []byte(`{"result":"success","time_last_update_utc":"Mon, 26 Oct 2026 00:02:31 +0000","rates":{"USD":1,"AMD":360.5}}`)
	q, err := parseMarket(body, Source{ID: "market", Name: "Market rate"})
	if err != nil {
		t.Fatal(err)
	}
	near(t, *q.Mid, 360.5)

	if _, err := parseMarket([]byte(`{"result":"error"}`), Source{}); err == nil {
		t.Fatal("expected error for result=error")
	}
	if _, err := parseMarket([]byte(`{"result":"success","rates":{}}`), Source{}); err == nil {
		t.Fatal("expected error for missing AMD")
	}
}

func TestParseCBA(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
 <soap:Body>
  <ExchangeRatesLatestResponse xmlns="http://www.cba.am/">
   <ExchangeRatesLatestResult>
    <CurrentDate>2026-10-26T00:00:00</CurrentDate>
    <Rates>
     <ExchangeRate><ISO>JPY</ISO><Amount>10</Amount><Rate>24.1</Rate><Difference>0.1</Difference></ExchangeRate>
     <ExchangeRate><ISO>USD</ISO><Amount>1</Amount><Rate>360.12</Rate><Difference>-0.2</Difference></ExchangeRate>
    </Rates>
   </ExchangeRatesLatestResult>
  </ExchangeRatesLatestResponse>
 </soap:Body>
</soap:Envelope>`)
	q, err := parseCBA(body, Source{ID: "cba", Name: "CBA"})
	if err != nil {
		t.Fatal(err)
	}
	near(t, *q.Mid, 360.12)
	if q.AsOf == "" {
		t.Fatal("expected as_of date")
	}

	fault := []byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultstring>boom</faultstring></soap:Fault></soap:Body></soap:Envelope>`)
	if _, err := parseCBA(fault, Source{}); err == nil {
		t.Fatal("expected SOAP fault error")
	}
}

func TestCBAPerUnit(t *testing.T) {
	body := []byte(`<Envelope><Body><ExchangeRatesLatestResponse><ExchangeRatesLatestResult><Rates>
	<ExchangeRate><ISO>USD</ISO><Amount>10</Amount><Rate>3600</Rate></ExchangeRate>
	</Rates></ExchangeRatesLatestResult></ExchangeRatesLatestResponse></Body></Envelope>`)
	q, err := parseCBA(body, Source{})
	if err != nil {
		t.Fatal(err)
	}
	near(t, *q.Mid, 360)
}

// Mirrors the structure of the real page: dropdown lists first, then the table.
const sasFixture = `<html><head><script>var x = "USD 1 2";</script></head><body>
<div class="exchange"><span>Առք</span>
<select><option>USD</option><option>AMD</option><option>USD1</option><option>RUR</option></select>
<input value="1"><input value="359">
<table><tr><th>Արժույթ</th><th>Առք</th><th>Վաճառք</th></tr>
<tr><td><img src="/flags/usd.png"> USD</td><td>359</td><td>361.5</td></tr>
<tr><td><img src="/flags/usd1.png"> USD1</td><td>357</td><td>359.5</td></tr>
</table>
<p>Թարմացվել է` + "`" + ` 26.10.2026</p></div></body></html>`

func TestParseSAS(t *testing.T) {
	q, err := parseSAS([]byte(sasFixture), Source{ID: "sas", Name: "SAS"})
	if err != nil {
		t.Fatal(err)
	}
	near(t, *q.Buy, 359)
	near(t, *q.Sell, 361.5)
	if q.AsOf != "26.10.2026" {
		t.Fatalf("as_of = %q", q.AsOf)
	}
	if _, err := parseSAS([]byte("<html>nothing here</html>"), Source{}); err == nil {
		t.Fatal("expected error when row is missing")
	}
}

func TestConvert(t *testing.T) {
	quotes := []Quote{
		{ID: "market", Name: "Market", Mid: f64(360)},
		{ID: "sas", Name: "SAS", Buy: f64(359), Sell: f64(361.5)},
		{ID: "broken", Name: "Broken", Error: "down"},
	}
	lines := Convert(130000, 165000, quotes)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	near(t, lines[0].USD, 361.11)
	near(t, lines[0].Toman, 130000.0/360*165000)
	near(t, lines[1].USD, 130000/361.5) // sell rate line
	near(t, lines[2].USD, 130000/359.0) // buy rate line

	if l := Convert(130000, 0, quotes); l[0].Toman != 0 {
		t.Fatal("toman should be omitted without a rate")
	}
}

func TestStoreCachesAndServesStale(t *testing.T) {
	calls, fail := 0, false
	src := Source{ID: "x", Name: "X", Fetch: func(ctx context.Context) (Quote, error) {
		calls++
		if fail {
			return Quote{}, errors.New("upstream down")
		}
		return Quote{ID: "x", Name: "X", Mid: f64(360)}, nil
	}}
	clock := time.Now()
	st := NewStore([]Source{src}, time.Minute)
	st.now = func() time.Time { return clock }

	st.Quotes(context.Background())
	st.Quotes(context.Background())
	if calls != 1 {
		t.Fatalf("cache miss: %d calls", calls)
	}

	// TTL expires and upstream is down: keep the old value, flag it stale.
	clock = clock.Add(2 * time.Minute)
	fail = true
	q := st.Quotes(context.Background())[0]
	if calls != 2 || q.Mid == nil || !q.Stale || q.Error == "" {
		t.Fatalf("expected stale value, got %+v (calls=%d)", q, calls)
	}

	// Failed sources are not hammered...
	st.Quotes(context.Background())
	if calls != 2 {
		t.Fatalf("retried too soon: %d calls", calls)
	}
	// ...but are retried after the backoff, and recover.
	clock = clock.Add(failedRetry + time.Second)
	fail = false
	q = st.Quotes(context.Background())[0]
	if calls != 3 || q.Stale || q.Error != "" {
		t.Fatalf("expected recovery, got %+v", q)
	}
}

func TestStoreFirstFetchFails(t *testing.T) {
	src := Source{ID: "x", Name: "X", Fetch: func(ctx context.Context) (Quote, error) {
		return Quote{}, errors.New("nope")
	}}
	q := NewStore([]Source{src}, time.Minute).Quotes(context.Background())[0]
	if q.Mid != nil || q.Error != "nope" || q.Name != "X" {
		t.Fatalf("unexpected quote %+v", q)
	}
}
