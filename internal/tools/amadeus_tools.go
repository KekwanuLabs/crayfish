package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/KekwanuLabs/crayfish/internal/security"
)

// AmadeusAuth manages OAuth2 client credentials for the Amadeus API.
// Thread-safe. Tokens are cached and refreshed 2 minutes before expiry.
type AmadeusAuth struct {
	mu       sync.Mutex
	clientID string
	secret   string
	token    string
	expiry   time.Time
	baseURL  string
}

// NewAmadeusAuth creates an Amadeus OAuth2 token manager with the given credentials.
func NewAmadeusAuth(clientID, secret string) *AmadeusAuth {
	return &AmadeusAuth{
		clientID: clientID,
		secret:   secret,
		baseURL:  "https://test.api.amadeus.com",
	}
}

// getToken returns a valid access token, refreshing if needed.
func (a *AmadeusAuth) getToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Return cached token if still valid (with 2-minute buffer).
	if a.token != "" && time.Now().Add(2*time.Minute).Before(a.expiry) {
		return a.token, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {a.clientID},
		"client_secret": {a.secret},
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		a.baseURL+"/v1/security/oauth2/token",
		strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("amadeus auth: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("amadeus auth: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("amadeus auth: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("amadeus auth: token request failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("amadeus auth: parse token: %w", err)
	}

	a.token = tokenResp.AccessToken
	a.expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return a.token, nil
}

// amadeusRequest makes an authenticated GET request to the Amadeus API.
func amadeusRequest(ctx context.Context, auth *AmadeusAuth, path string, query url.Values) (json.RawMessage, error) {
	token, err := auth.getToken(ctx)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 15 * time.Second}
	reqURL := auth.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("amadeus: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("amadeus: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, fmt.Errorf("amadeus: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Parse Amadeus error for a friendlier message.
		var errResp struct {
			Errors []struct {
				Detail string `json:"detail"`
				Code   int    `json:"code"`
			} `json:"errors"`
		}
		if json.Unmarshal(body, &errResp) == nil && len(errResp.Errors) > 0 {
			return nil, fmt.Errorf("amadeus API error: %s", errResp.Errors[0].Detail)
		}
		return nil, fmt.Errorf("amadeus API returned HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 512)]))
	}

	return json.RawMessage(body), nil
}

// AmadeusConnectDeps holds dependencies for the amadeus_connect tool.
type AmadeusConnectDeps struct {
	IsConfigured func() bool
	SaveCreds    func(clientID, clientSecret string)
	Registry     *Registry
}

// RegisterAmadeusConnectTool adds the amadeus_connect setup tool.
func RegisterAmadeusConnectTool(reg *Registry, deps AmadeusConnectDeps) {
	reg.logger.Info("registering amadeus_connect tool")

	reg.Register(&Tool{
		Name:        "amadeus_connect",
		Description: "Set up flight search by adding Amadeus API credentials. Walk the user through getting free test credentials from developers.amadeus.com, then verify and activate flight tools.",
		MinTier:     security.TierOperator,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"client_id": {
					"type": "string",
					"description": "Amadeus API client ID"
				},
				"client_secret": {
					"type": "string",
					"description": "Amadeus API client secret"
				}
			}
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				ClientID     string `json:"client_id"`
				ClientSecret string `json:"client_secret"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("amadeus_connect: parse input: %w", err)
			}

			if params.ClientID == "" || params.ClientSecret == "" {
				if deps.IsConfigured() {
					return "Flight search is already configured and working. No action needed.", nil
				}
				return `Flight search is not set up yet. To enable it, the user needs free Amadeus API credentials:

1. Go to https://developers.amadeus.com/
2. Create a free account and register an app
3. Copy the API Key (client ID) and API Secret (client secret)
4. The free test tier gives 2,000 flight searches per month

Once the user has both the API Key and API Secret, call this tool again with client_id and client_secret to verify and activate flight search.`, nil
			}

			// Verify credentials by attempting token exchange.
			auth := NewAmadeusAuth(strings.TrimSpace(params.ClientID), strings.TrimSpace(params.ClientSecret))
			_, err := auth.getToken(ctx)
			if err != nil {
				if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
					return "Those credentials were rejected by Amadeus. Please double-check the API Key and API Secret from https://developers.amadeus.com/", nil
				}
				return fmt.Sprintf("Could not verify credentials: %v. Check your internet connection and try again.", err), nil
			}

			// Credentials work — save and register tools.
			deps.SaveCreds(strings.TrimSpace(params.ClientID), strings.TrimSpace(params.ClientSecret))
			RegisterAmadeusTools(deps.Registry, auth)

			return "Flight search is now active! Credentials verified and saved. You can now search flights, find cheapest dates, and analyze prices.", nil
		},
	})
}

// RegisterAmadeusTools adds the flight search tools to the registry.
func RegisterAmadeusTools(reg *Registry, auth *AmadeusAuth) {
	reg.logger.Info("registering Amadeus flight tools")

	// flight_search — search for flight offers.
	reg.Register(&Tool{
		Name:        "flight_search",
		Description: "Search for flight offers between two cities on a specific date. Returns prices, airlines, duration, and number of stops.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"origin": {
					"type": "string",
					"description": "Origin airport IATA code (e.g., JFK, LAX, ORD)"
				},
				"destination": {
					"type": "string",
					"description": "Destination airport IATA code (e.g., HNL, CDG, NRT)"
				},
				"departure_date": {
					"type": "string",
					"description": "Departure date in YYYY-MM-DD format"
				},
				"return_date": {
					"type": "string",
					"description": "Return date in YYYY-MM-DD format (optional for one-way)"
				},
				"adults": {
					"type": "integer",
					"description": "Number of adult passengers (default: 1)"
				},
				"max_results": {
					"type": "integer",
					"description": "Maximum number of results (default: 5, max: 10)"
				}
			},
			"required": ["origin", "destination", "departure_date"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Origin        string `json:"origin"`
				Destination   string `json:"destination"`
				DepartureDate string `json:"departure_date"`
				ReturnDate    string `json:"return_date"`
				Adults        int    `json:"adults"`
				MaxResults    int    `json:"max_results"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("flight_search: parse input: %w", err)
			}
			if params.Origin == "" || params.Destination == "" || params.DepartureDate == "" {
				return "", fmt.Errorf("flight_search: origin, destination, and departure_date are required")
			}

			adults := 1
			if params.Adults > 0 {
				adults = params.Adults
			}
			maxResults := 5
			if params.MaxResults > 0 && params.MaxResults <= 10 {
				maxResults = params.MaxResults
			}

			query := url.Values{
				"originLocationCode":      {strings.ToUpper(params.Origin)},
				"destinationLocationCode": {strings.ToUpper(params.Destination)},
				"departureDate":           {params.DepartureDate},
				"adults":                  {fmt.Sprintf("%d", adults)},
				"max":                     {fmt.Sprintf("%d", maxResults)},
				"currencyCode":            {"USD"},
			}
			if params.ReturnDate != "" {
				query.Set("returnDate", params.ReturnDate)
			}

			body, err := amadeusRequest(ctx, auth, "/v2/shopping/flight-offers", query)
			if err != nil {
				return "", fmt.Errorf("flight_search: %w", err)
			}

			return formatFlightOffers(body, params.Origin, params.Destination, params.DepartureDate)
		},
	})

	// flight_cheapest_dates — find cheapest travel dates for a route.
	reg.Register(&Tool{
		Name:        "flight_cheapest_dates",
		Description: "Find the cheapest travel dates for a route. Great for flexible travelers and price watching — shows which dates have the lowest fares.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"origin": {
					"type": "string",
					"description": "Origin airport IATA code"
				},
				"destination": {
					"type": "string",
					"description": "Destination airport IATA code"
				}
			},
			"required": ["origin", "destination"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Origin      string `json:"origin"`
				Destination string `json:"destination"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("flight_cheapest_dates: parse input: %w", err)
			}
			if params.Origin == "" || params.Destination == "" {
				return "", fmt.Errorf("flight_cheapest_dates: origin and destination are required")
			}

			query := url.Values{
				"origin":      {strings.ToUpper(params.Origin)},
				"destination": {strings.ToUpper(params.Destination)},
			}

			body, err := amadeusRequest(ctx, auth, "/v1/shopping/flight-dates", query)
			if err != nil {
				return "", fmt.Errorf("flight_cheapest_dates: %w", err)
			}

			return formatCheapestDates(body, params.Origin, params.Destination)
		},
	})

	// flight_price_analysis — analyze if a price is high, typical, or low.
	reg.Register(&Tool{
		Name:        "flight_price_analysis",
		Description: "Analyze whether a flight price is HIGH, TYPICAL, or LOW compared to historical data. Use this after searching flights to help the user decide if they should book now or wait.",
		MinTier:     security.TierTrusted,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"origin": {
					"type": "string",
					"description": "Origin airport IATA code"
				},
				"destination": {
					"type": "string",
					"description": "Destination airport IATA code"
				},
				"departure_date": {
					"type": "string",
					"description": "Departure date in YYYY-MM-DD format"
				},
				"currency_code": {
					"type": "string",
					"description": "Currency code (default: USD)"
				}
			},
			"required": ["origin", "destination", "departure_date"]
		}`),
		Execute: func(ctx context.Context, sess *security.Session, input json.RawMessage) (string, error) {
			var params struct {
				Origin        string `json:"origin"`
				Destination   string `json:"destination"`
				DepartureDate string `json:"departure_date"`
				CurrencyCode  string `json:"currency_code"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return "", fmt.Errorf("flight_price_analysis: parse input: %w", err)
			}
			if params.Origin == "" || params.Destination == "" || params.DepartureDate == "" {
				return "", fmt.Errorf("flight_price_analysis: origin, destination, and departure_date are required")
			}

			currency := "USD"
			if params.CurrencyCode != "" {
				currency = strings.ToUpper(params.CurrencyCode)
			}

			query := url.Values{
				"originIataCode":      {strings.ToUpper(params.Origin)},
				"destinationIataCode": {strings.ToUpper(params.Destination)},
				"departureDate":       {params.DepartureDate},
				"currencyCode":        {currency},
			}

			body, err := amadeusRequest(ctx, auth, "/v1/analytics/itinerary-price-metrics", query)
			if err != nil {
				return "", fmt.Errorf("flight_price_analysis: %w", err)
			}

			return formatPriceAnalysis(body, params.Origin, params.Destination, params.DepartureDate)
		},
	})
}

// --- Response formatting helpers ---

func formatFlightOffers(body json.RawMessage, origin, dest, date string) (string, error) {
	var resp struct {
		Data []struct {
			Price struct {
				Total    string `json:"total"`
				Currency string `json:"currency"`
			} `json:"price"`
			Itineraries []struct {
				Duration string `json:"duration"`
				Segments []struct {
					Departure struct {
						IataCode string `json:"iataCode"`
						At       string `json:"at"`
					} `json:"departure"`
					Arrival struct {
						IataCode string `json:"iataCode"`
						At       string `json:"at"`
					} `json:"arrival"`
					CarrierCode string `json:"carrierCode"`
					Number      string `json:"number"`
				} `json:"segments"`
			} `json:"itineraries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse flight offers: %w", err)
	}

	if len(resp.Data) == 0 {
		return fmt.Sprintf("No flights found from %s to %s on %s. Try different dates or check the airport codes.", origin, dest, date), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Flight offers from %s to %s on %s:\n\n", origin, dest, date))

	for i, offer := range resp.Data {
		sb.WriteString(fmt.Sprintf("%d. $%s %s", i+1, offer.Price.Total, offer.Price.Currency))

		for j, itin := range offer.Itineraries {
			label := "Outbound"
			if j == 1 {
				label = "Return"
			}
			stops := len(itin.Segments) - 1
			stopText := "nonstop"
			if stops == 1 {
				stopText = "1 stop"
			} else if stops > 1 {
				stopText = fmt.Sprintf("%d stops", stops)
			}

			// Format duration: PT2H30M → 2h30m
			duration := strings.ToLower(strings.TrimPrefix(itin.Duration, "PT"))

			sb.WriteString(fmt.Sprintf("\n   %s: %s (%s)", label, duration, stopText))

			if len(itin.Segments) > 0 {
				first := itin.Segments[0]
				last := itin.Segments[len(itin.Segments)-1]
				carriers := make(map[string]bool)
				for _, seg := range itin.Segments {
					carriers[seg.CarrierCode] = true
				}
				carrierList := make([]string, 0, len(carriers))
				for c := range carriers {
					carrierList = append(carrierList, c)
				}
				sb.WriteString(fmt.Sprintf("\n   %s → %s | %s",
					first.Departure.IataCode, last.Arrival.IataCode,
					strings.Join(carrierList, ", ")))
			}
		}
		sb.WriteString("\n\n")
	}

	return sb.String(), nil
}

func formatCheapestDates(body json.RawMessage, origin, dest string) (string, error) {
	var resp struct {
		Data []struct {
			DepartureDate string `json:"departureDate"`
			ReturnDate    string `json:"returnDate"`
			Price         struct {
				Total string `json:"total"`
			} `json:"price"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse cheapest dates: %w", err)
	}

	if len(resp.Data) == 0 {
		return fmt.Sprintf("No cheapest date data found for %s to %s. This route may not have enough data.", origin, dest), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Cheapest travel dates from %s to %s:\n\n", origin, dest))

	for i, d := range resp.Data {
		if i >= 10 {
			break
		}
		if d.ReturnDate != "" {
			sb.WriteString(fmt.Sprintf("• %s → %s: $%s\n", d.DepartureDate, d.ReturnDate, d.Price.Total))
		} else {
			sb.WriteString(fmt.Sprintf("• %s: $%s\n", d.DepartureDate, d.Price.Total))
		}
	}

	return sb.String(), nil
}

func formatPriceAnalysis(body json.RawMessage, origin, dest, date string) (string, error) {
	var resp struct {
		Data []struct {
			PriceMetrics []struct {
				Amount       string `json:"amount"`
				QuartileRanking string `json:"quartileRanking"`
			} `json:"priceMetrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse price analysis: %w", err)
	}

	if len(resp.Data) == 0 {
		return fmt.Sprintf("No price analysis data available for %s to %s on %s. The route may not have enough historical data.", origin, dest, date), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Price analysis for %s to %s on %s:\n\n", origin, dest, date))

	for _, item := range resp.Data {
		for _, metric := range item.PriceMetrics {
			label := metric.QuartileRanking
			switch label {
			case "MINIMUM":
				label = "Lowest seen"
			case "FIRST":
				label = "Low (25th percentile)"
			case "MEDIUM":
				label = "Typical (median)"
			case "THIRD":
				label = "High (75th percentile)"
			case "MAXIMUM":
				label = "Highest seen"
			}
			sb.WriteString(fmt.Sprintf("• %s: $%s\n", label, metric.Amount))
		}
	}

	sb.WriteString("\nCompare current prices against these ranges to decide if it's a good time to book.")

	return sb.String(), nil
}
